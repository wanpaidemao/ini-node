// forklocate 定位本地 header 链与全网主链的分叉点(只读)。
//
// 数据来源双模式:
//
//	-rpc   :通过节点 RPC(getblockchaininfo/getblockhash)读本地高度->哈希,
//	        节点可保持运行,无数据库锁冲突,生产环境推荐;
//	-dbpath:直接打开本地 blocks_ffldb 读高度索引(需停止节点)。
//	peer 侧一律以 P2P 出站连接参考节点(用 getheaders 拉连续 header 段)。
//
// 定位算法(拉段对比):
//  1. 构建"由近及远、指数回退"的多点 locator,一次 getheaders 拿到
//     peer 从"最后共同点+1"起的连续 header 段
//  2. 用段首头 PrevBlock 还原共同高度 c;逐高度对比 peer 段与本地哈希,
//     第一个不一致高度的前一个高度即分叉点 X
//  3. 段耗尽仍未分叉时,以段尾为 locator 续拍,直到定位或到 peer tip
//
// 用法:
//
//	forklocate -rpc 127.0.0.1:8334 -rpcuser sugar -rpcpass <pw> -peer <host:port> [-lo 43760164]
//	forklocate -dbpath <blocks_ffldb> -peer <host:port> [-lo 43760164]
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	_ "github.com/btcsuite/btcd/database/ffldb"
	"github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/wire/v2"
)

// chainOracle exposes the local chain's height->hash and tip height, backed
// either by the database (dbpath mode) or the node RPC (rpc mode).
type chainOracle struct {
	db               database.DB // nil in rpc mode
	rpcURL           string
	rpcUser, rpcPass string
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (o *chainOracle) rpc(method string, params ...interface{}) ([]byte, error) {
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", o.rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(o.rpcUser, o.rpcPass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var rr rpcResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, err
	}
	if len(rr.Error) > 0 && string(rr.Error) != "null" {
		return nil, fmt.Errorf("rpc %s: %s", method, rr.Error)
	}
	return rr.Result, nil
}

// hashAt returns the local header hash at the given height, or (nil,false)
// when unknown.
func (o *chainOracle) hashAt(height int32) (*chainhash.Hash, bool) {
	if o.db != nil {
		var h *chainhash.Hash
		err := o.db.View(func(dbTx database.Tx) error {
			hi := dbTx.Metadata().Bucket([]byte("heightidx"))
			if hi == nil {
				return fmt.Errorf("height index bucket missing")
			}
			var key [4]byte
			binary.LittleEndian.PutUint32(key[:], uint32(height))
			v := hi.Get(key[:])
			if len(v) != chainhash.HashSize {
				return nil
			}
			hh, err := chainhash.NewHash(v)
			if err != nil {
				return err
			}
			h = hh
			return nil
		})
		if err != nil || h == nil {
			return nil, false
		}
		return h, true
	}

	res, err := o.rpc("getblockhash", int64(height))
	if err != nil {
		return nil, false
	}
	var hs string
	if err := json.Unmarshal(res, &hs); err != nil || hs == "" {
		return nil, false
	}
	h, err := chainhash.NewHashFromStr(hs)
	if err != nil {
		return nil, false
	}
	return h, true
}

func (o *chainOracle) tip() (int32, error) {
	if o.db != nil {
		var tip int32
		err := o.db.View(func(dbTx database.Tx) error {
			raw := dbTx.Metadata().Get([]byte("chainstate"))
			if raw == nil {
				return fmt.Errorf("chainstate missing")
			}
			if len(raw) < 36 {
				return fmt.Errorf("chainstate too short")
			}
			tip = int32(binary.LittleEndian.Uint32(raw[32:36]))
			return nil
		})
		return tip, err
	}

	res, err := o.rpc("getblockchaininfo")
	if err != nil {
		return 0, err
	}
	var info struct {
		Blocks  int64 `json:"blocks"`
		Headers int64 `json:"headers"`
	}
	if err := json.Unmarshal(res, &info); err != nil {
		return 0, err
	}
	// Use min(blocks, headers): headers may outrun blocks during IBD, and the
	// connected chain is the one that matters for fork location.
	tip := info.Headers
	if info.Blocks < info.Headers {
		tip = info.Blocks
	}
	if tip < 0 {
		tip = 0
	}
	return int32(tip), nil
}

// refLocator builds a "recent-first, exponentially-spaced" locator covering
// [lo, tip] so a single getheaders lands on the deepest shared ancestor.
func refLocator(o *chainOracle, tip, lo int32) (blockchain.BlockLocator, []int32) {
	var loc blockchain.BlockLocator
	var hs []int32
	h := tip
	step := int32(1)
	for h >= lo && len(loc) < 80 {
		if hash, ok := o.hashAt(h); ok {
			loc = append(loc, hash)
			hs = append(hs, h)
		}
		h -= step
		if step < 10000 {
			step *= 2
		}
	}
	return loc, hs
}

func main() {
	var (
		dbPath   string
		rpcAddr  string
		rpcUser  string
		rpcPass  string
		peerAddr string
		isTLS    bool
		lo       int
		timeout  time.Duration
	)
	flag.StringVar(&dbPath, "dbpath", "", "path to blocks_ffldb (direct read; stop the node first)")
	flag.StringVar(&rpcAddr, "rpc", "", "node RPC host:port (read local chain via RPC; node may keep running)")
	flag.StringVar(&rpcUser, "rpcuser", "", "RPC user (default: ini)")
	flag.StringVar(&rpcPass, "rpcpass", "", "RPC password")
	flag.BoolVar(&isTLS, "rpctls", false, "use https for RPC")
	flag.StringVar(&peerAddr, "peer", "", "reference peer address host:port (prefer the Umami reference node)")
	flag.IntVar(&lo, "lo", 43760164, "low bound guaranteed to be on the real main chain (default: addcheckpoint)")
	flag.DurationVar(&timeout, "timeout", 10*time.Second, "per-request timeout")
	flag.Parse()

	if (dbPath == "") == (rpcAddr == "") {
		fmt.Fprintln(os.Stderr, "exactly one of -dbpath / -rpc is required")
		fmt.Fprintln(os.Stderr, "usage: forklocate {-dbpath <dir> | -rpc <host:port> -rpcpass <pw>} -peer <host:port> [-lo 43760164]")
		os.Exit(2)
	}
	if peerAddr == "" {
		fmt.Fprintln(os.Stderr, "-peer is required")
		os.Exit(2)
	}

	var o *chainOracle
	if dbPath != "" {
		if _, err := os.Stat(dbPath); err != nil {
			fmt.Fprintln(os.Stderr, "dbpath error:", err)
			os.Exit(1)
		}
		db, err := database.Open("ffldb", dbPath, chaincfg.MainNetParams.Net)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open db error:", err)
			os.Exit(1)
		}
		defer db.Close()
		o = &chainOracle{db: db}
	} else {
		if rpcUser == "" {
			rpcUser = "ini"
		}
		scheme := "http"
		if isTLS {
			scheme = "https"
		}
		o = &chainOracle{
			rpcURL:  fmt.Sprintf("%s://%s", scheme, rpcAddr),
			rpcUser: rpcUser,
			rpcPass: rpcPass,
		}
	}

	tip, err := o.tip()
	if err != nil {
		fmt.Fprintln(os.Stderr, "read tip error:", err)
		os.Exit(1)
	}
	if lo < 0 {
		lo = 0
	}
	fmt.Printf("local tip height=%d  low bound=%d\n", tip, lo)
	if tip <= int32(lo) {
		fmt.Fprintln(os.Stderr, "local tip is not above the low bound; nothing to locate")
		os.Exit(3)
	}

	// Connect to the reference peer.
	conn, err := net.DialTimeout("tcp", peerAddr, timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial peer error:", err)
		os.Exit(1)
	}

	locHash, _ := o.hashAt(tip)
	if locHash == nil {
		fmt.Fprintln(os.Stderr, "local tip hash not resolvable")
		os.Exit(1)
	}

	hdrsCh := make(chan []*wire.BlockHeader, 1)
	drain := func() {
		select {
		case <-hdrsCh:
		default:
		}
	}
	handshakeDone := make(chan struct{})
	var handshakeOnce bool
	peerCfg := &peer.Config{
		NewestBlock: func() (*chainhash.Hash, int32, error) {
			return locHash, tip, nil
		},
		ChainParams:      &chaincfg.MainNetParams,
		Services:         0,
		UserAgentName:    "forklocate",
		UserAgentVersion: "0.0.1",
		ProtocolVersion:  peer.MaxProtocolVersion,
		Listeners: peer.MessageListeners{
			OnVerAck: func(p *peer.Peer, msg *wire.MsgVerAck) {
				if !handshakeOnce {
					handshakeOnce = true
					close(handshakeDone)
				}
			},
			OnHeaders: func(p *peer.Peer, msg *wire.MsgHeaders) {
				select {
				case hdrsCh <- msg.Headers:
				default:
				}
			},
		},
	}
	p, err := peer.NewOutboundPeer(peerCfg, peerAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "new outbound peer error:", err)
		os.Exit(1)
	}
	p.AssociateConnection(conn)
	defer p.Disconnect()

	select {
	case <-handshakeDone:
	case <-time.After(timeout):
		fmt.Fprintln(os.Stderr, "peer handshake timed out")
		os.Exit(1)
	}
	fmt.Printf("connected to %s (subver=%q)\n", peerAddr, p.UserAgent())

	ask := func(loc blockchain.BlockLocator) []*wire.BlockHeader {
		drain()
		var stop chainhash.Hash
		if err := p.PushGetHeadersMsg(loc, &stop); err != nil {
			return nil
		}
		select {
		case hdrs := <-hdrsCh:
			return hdrs
		case <-time.After(timeout):
			return nil
		case <-p.Done():
			return nil
		}
	}

	// Locate: pull peer segments and walk locally until the first mismatch.
	loc, locH := refLocator(o, tip, int32(lo))
	if len(loc) == 0 {
		fmt.Fprintln(os.Stderr, "cannot build a locator from the local chain")
		os.Exit(1)
	}

	fork := int32(-1)
	var invalidHash *chainhash.Hash
	round := 0
	for {
		round++
		if round > 6 {
			fmt.Fprintln(os.Stderr, "too many segment rounds; peer segment never diverges?")
			os.Exit(1)
		}

		hdrs := ask(loc)
		if len(hdrs) == 0 {
			fmt.Fprintln(os.Stderr, "peer returned no headers; cannot locate (peer uncooperative?)")
			os.Exit(1)
		}

		prev := hdrs[0].PrevBlock
		segBase := int32(-1)
		for i, H := range loc {
			if H.IsEqual(&prev) {
				segBase = locH[i]
				break
			}
		}
		if segBase < 0 {
			fmt.Fprintf(os.Stderr,
				"segment first header prev %v not in the local locator;\n"+
					"likely a real fork deeper than the locator's reach (below height %d).\n",
				prev, locH[len(locH)-1])
			os.Exit(1)
		}

		h := segBase + 1
		for i := 0; i < len(hdrs) && h <= tip; i++ {
			lh, ok := o.hashAt(h)
			if !ok {
				fmt.Fprintf(os.Stderr, "local chain unresolvable at height %d\n", h)
				os.Exit(1)
			}
			if hdrs[i].BlockHash() != *lh {
				fork = h - 1
				invalidHash = lh
				break
			}
			h++
		}

		if fork >= 0 {
			break
		}
		if h > tip {
			fmt.Println("NO_INVALIDATE (local tip is on the network main chain)")
			return
		}
		fmt.Printf("segment matched up to %d, continuing...\n", h-1)
		next, ok := o.hashAt(h - 1)
		if !ok {
			fmt.Fprintf(os.Stderr, "local chain unresolvable at height %d\n", h-1)
			os.Exit(1)
		}
		loc = blockchain.BlockLocator{next}
		locH = []int32{h - 1}
	}

	fmt.Printf("\nrounds=%d\n", round)
	fmt.Printf("FORK_POINT=%d  (last height local agrees with network)\n", fork)
	if invalidHash == nil {
		fmt.Println("NO_INVALIDATE (local tip is on the network main chain)")
	} else {
		fmt.Printf("INVALIDATE_HASH=%v  (height %d) -- local block at fork+1 to invalidate\n",
			invalidHash, fork+1)
		fmt.Println()
		fmt.Println("recovery (requires a running node):")
		fmt.Printf("  ini-cli invalidateblock %v\n", invalidHash)
	}
}
