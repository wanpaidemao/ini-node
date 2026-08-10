package sugarindex

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btclog"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// indexTipKey 是 sugar index 自己用的尖点标记键(以 '\x00' 开头,与 umami 的
// 1 字节前缀索引键 'a'/'u'/'p'/'s' 及 '\x00obfuscate_key' 均不冲突)。
// indexTipKey is the tip marker owned by the sugar index. It starts with
// '\x00' so it can never collide with umami's single-byte prefixed index keys
// or the '\x00obfuscate_key' key.
var indexTipKey = []byte("\x00btcd_sugarindex_tip")

// openIndexDB 打开(必要时创建)位于 path 的 raw LevelDB,并加载或生成 8 字节
// 混淆密钥。目录不存在时自动创建。
// openIndexDB opens (creating if needed) the raw LevelDB at path and loads or
// generates the 8-byte obfuscation key.
func openIndexDB(path string, log btclog.Logger) (*leveldb.DB, []byte, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, nil, fmt.Errorf("sugarindex: create dir %q: %w", path, err)
	}

	opts := &opt.Options{
		Filter: filter.NewBloomFilter(10),
		// Open files with write access so the DB remains writable even
		// though we only read during startup catchup.
		ReadOnly: false,
	}
	ldb, err := leveldb.OpenFile(path, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("sugarindex: open %q: %w", path, err)
	}

	key, err := loadObfuscateKey(ldb)
	if err != nil {
		ldb.Close()
		return nil, nil, err
	}
	if log != nil {
		log.Infof("Sugar index enabled at %q (obfuscation key %x)", path, key)
	}
	return ldb, key, nil
}

// loadObfuscateKey 读取 '\x00obfuscate_key' 下的 8 字节密钥;若不存在则生成并
// 持久化(与 umami dbwrapper.cpp 的 GetObfuscationKey 一致)。
// loadObfuscateKey reads the 8-byte key stored under '\x00obfuscate_key',
// generating and persisting one when absent (mirrors GetObfuscationKey in
// umami's src/dbwrapper.cpp).
func loadObfuscateKey(ldb *leveldb.DB) ([]byte, error) {
	key, err := ldb.Get(obfuscateKeyName, nil)
	if err == nil {
		if len(key) == obfuscateKeyNumByte {
			return key, nil
		}
		return nil, fmt.Errorf("sugarindex: invalid obfuscation key length %d", len(key))
	}
	if err != leveldb.ErrNotFound {
		return nil, fmt.Errorf("sugarindex: read obfuscation key: %w", err)
	}

	newKey := make([]byte, obfuscateKeyNumByte)
	if _, err := rand.Read(newKey); err != nil {
		return nil, fmt.Errorf("sugarindex: generate obfuscation key: %w", err)
	}
	if err := ldb.Put(obfuscateKeyName, newKey, nil); err != nil {
		return nil, fmt.Errorf("sugarindex: write obfuscation key: %w", err)
	}
	return newKey, nil
}

// obfuscate 对值字节做 XOR 混淆(仅值,键不混淆),与 umami streams.h 一致。
// obfuscate XOR-obfuscates value bytes only (keys are never obfuscated),
// matching umami's streams.h.
func (m *Manager) obfuscate(value []byte) []byte { return xorObfuscate(m.key, value) }

// deobfuscate 还原混淆,与 obfuscate 自反。
// deobfuscate reverses the obfuscation (XOR is self-inverse).
func (m *Manager) deobfuscate(value []byte) []byte { return xorObfuscate(m.key, value) }

// putObfuscated 将序列化后的值混淆后写入批次。
// putObfuscated obfuscates and adds a serialized value to the batch.
func (m *Manager) putObfuscated(batch *leveldb.Batch, key, value []byte) {
	batch.Put(key, m.obfuscate(value))
}

// getRaw 直接读取键对应的混淆值,不还原;返回 nil 表示不存在。
// getRaw reads a raw (still obfuscated) value for a key, nil when absent.
func (m *Manager) getRaw(key []byte) ([]byte, error) {
	value, err := m.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

// getValue 读取键并还原混淆;不存在时返回 (nil, nil)。
// getValue reads and deobfuscates a value, (nil, nil) when absent.
func (m *Manager) getValue(key []byte) ([]byte, error) {
	value, err := m.getRaw(key)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	return m.deobfuscate(value), nil
}

// iteratePrefix 对前缀内的键值对按序回调。fn 返回 false 时停止迭代。
// iteratePrefix calls fn in order for every key/value under the prefix,
// stopping when fn returns false.
func (m *Manager) iteratePrefix(prefix []byte, fn func(key, value []byte) bool) error {
	iter := m.db.NewIterator(&util.Range{Start: prefix, Limit: prefixSuccessor(prefix)}, nil)
	defer iter.Release()

	for iter.Next() {
		key := iter.Key()
		value := iter.Value()
		if !fn(key, value) {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("sugarindex: iterate %q: %w", prefix, err)
	}
	return nil
}

// iteratePrefixDeobf returns iteratePrefix with deobfuscated values.
func (m *Manager) iteratePrefixDeobf(prefix []byte, fn func(key, value []byte) bool) error {
	return m.iteratePrefix(prefix, func(key, value []byte) bool {
		return fn(key, m.deobfuscate(value))
	})
}

// prefixSuccessor 返回恰好大于 prefix 的最小字节切片,用于 Range.Limit。
// prefixSuccessor returns the smallest byte slice strictly greater than prefix.
func prefixSuccessor(prefix []byte) []byte {
	succ := append([]byte{}, prefix...)
	for i := len(succ) - 1; i >= 0; i-- {
		if succ[i] < 0xff {
			succ[i]++
			return succ[:i+1]
		}
	}
	return nil // 全 0xff,range 会到尽头
}

// fetchIndexTip 读取本地尖点(block hash + height)。未初始化返回 nil hash。
// fetchIndexTip reads the local tip (block hash + height), nil hash if absent.
func (m *Manager) fetchIndexTip() (*chainhash.Hash, int32, error) {
	raw, err := m.getValue(indexTipKey)
	if err != nil {
		return nil, 0, err
	}
	if raw == nil {
		return nil, -1, nil
	}
	var h chainhash.Hash
	copy(h[:], raw[:32])
	height := int32(binary.LittleEndian.Uint32(raw[32:36]))
	return &h, height, nil
}

// storeIndexTip 记录本地尖点(block hash + height)。
// storeIndexTip records the local tip (block hash + height).
func (m *Manager) storeIndexTip(batch *leveldb.Batch, hash *chainhash.Hash, height int32) {
	e := &enc{}
	e.hash(*hash)
	e.i32(height)
	m.putObfuscated(batch, indexTipKey, e.bytes())
}
