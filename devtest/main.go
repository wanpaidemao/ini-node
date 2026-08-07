package main

import (
	"fmt"
	"math/rand"
	"net"
	"os"
	"time"

	"github.com/btcsuite/btcd/wire/v2"
)

func main() {
	host := os.Args[1]

	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		fmt.Println("dial err:", err)
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))

	peerIP, _, _ := net.SplitHostPort(host)
	pip := net.ParseIP(peerIP)
	theirNA := wire.NewNetAddressIPPort(pip, 34230, 0)
	ourNA := &wire.NetAddress{Services: wire.SFNodeNetwork}
	nonce := uint64(rand.Int63())

	msg := wire.NewMsgVersion(ourNA, theirNA, nonce, 0)
	msg.ProtocolVersion = int32(wire.ProtocolVersion)
	msg.AddUserAgent("btcd", "0.26.2")
	netv := wire.BitcoinNet(0x9d4beb9f)
	if err := wire.WriteMessage(conn, msg, wire.ProtocolVersion, netv); err != nil {
		fmt.Println("write msg err:", err)
		return
	}
	fmt.Println("wrote version, pver =", wire.ProtocolVersion,
		" agent=", msg.UserAgent)

	rmsg, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, netv)
	if err != nil {
		fmt.Println("read reply err (EOF likely refusal):", err)
		return
	}
	if v, ok := rmsg.(*wire.MsgVersion); ok {
		fmt.Println("REMOTE VERSION agent=", v.UserAgent,
			" pver=", v.ProtocolVersion, " lastBlock=", v.LastBlock,
			" services=", v.Services)
	} else {
		fmt.Printf("got cmd %T\n", rmsg)
	}
}
