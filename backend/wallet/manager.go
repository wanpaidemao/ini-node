package wallet

import (
	"fmt"
	"os"
	"sync"

	"github.com/btcsuite/btcd/chaincfg/v2"
)

// Manager owns the built-in wallet lifecycle: it tracks the on-disk wallet
// file path, the network parameters, and the currently unlocked wallet. It is
// safe for concurrent use.
// Manager 管理内置钱包生命周期：记录钱包文件路径、网络参数与当前已解锁钱包。
// 支持并发访问。
type Manager struct {
	mtx      sync.Mutex       // guards the fields below / 保护以下字段
	path     string           // wallet.db path / 钱包文件路径
	net      *chaincfg.Params // network parameters / 网络参数
	unlocked *Wallet          // unlocked wallet, nil if locked / 已解锁钱包（锁定时为 nil）
}

// NewManager creates a wallet manager rooted at path (e.g. <datadir>/wallet.db).
// NewManager 创建以 path（如 <datadir>/wallet.db）为钱包文件的管理器。
func NewManager(path string, net *chaincfg.Params) *Manager {
	return &Manager{path: path, net: net}
}

// Exists reports whether a wallet file is present on disk.
// Exists 报告磁盘上是否已存在钱包文件。
func (m *Manager) Exists() bool {
	_, err := os.Stat(m.path)
	return err == nil
}

// Path returns the wallet file path.
// Path 返回钱包文件路径。
func (m *Manager) Path() string {
	return m.path
}

// Create generates a new HD wallet from a fresh mnemonic, saves it encrypted
// with passphrase, and leaves it unlocked. It returns the mnemonic once so the
// caller can present it for backup.
// Create 用全新助记词生成 HD 钱包，以口令加密保存并保持解锁；返回助记词供备份展示。
func (m *Manager) Create(passphrase string) (mnemonic string, w *Wallet, err error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.unlocked != nil {
		return "", nil, fmt.Errorf("wallet is already unlocked / 钱包已解锁")
	}
	if m.Exists() {
		return "", nil, fmt.Errorf("wallet already exists at %s / 钱包已存在: %s", m.path, m.path)
	}
	mnemonic, err = GenerateMnemonic(TwelveWords)
	if err != nil {
		return "", nil, err
	}
	seed := MnemonicToSeed(mnemonic, "")
	w, err = NewFromSeed(seed, m.net)
	zeroBytes(seed) // clear the temporary seed copy / 清空临时种子副本
	if err != nil {
		return "", nil, err
	}
	if err := w.SaveWallet(m.path, passphrase); err != nil {
		return "", nil, err
	}
	_ = m.saveNextIndexFor(0, false) // ensure the sidecar exists / 确保旁车文件存在
	m.unlocked = w
	return mnemonic, w, nil
}

// Unlock loads and decrypts the wallet file with passphrase, leaving it
// unlocked. A wrong passphrase returns an error.
// Unlock 用口令加载并解密钱包文件并保持解锁；口令错误返回错误。
func (m *Manager) Unlock(passphrase string) (*Wallet, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.unlocked != nil {
		return m.unlocked, nil
	}
	w, err := UnlockWallet(m.path, passphrase, m.net)
	if err != nil {
		return nil, err
	}
	w.SetNextIndex(m.loadNextIndexFor(false)) // restore persisted address index / 恢复持久化的地址索引
	m.unlocked = w
	return w, nil
}

// Login derives a wallet from the legacy email/password KDF purely in memory
// and leaves it unlocked. Unlike Create/Unlock it never touches wallet.db, so
// legacy login is fully isolated from the BIP39 wallet (no cross-overwrite).
// Its next-address index lives in a separate sidecar, so index 0 (the original
// web-wallet address) is always the first address handed out.
// Login 用传统邮箱密码 KDF 纯内存派生钱包并保持解锁。与 Create/Unlock 不同，它
// 不碰 wallet.db，与 BIP39 钱包完全隔离（互不覆盖）。其下一地址索引存放于独立
// 旁车文件，从而 index 0（原 web-wallet 地址）始终是第一个分配的地址。
func (m *Manager) Login(email, password string) (*Wallet, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.unlocked != nil {
		return nil, fmt.Errorf("wallet is already unlocked / 钱包已解锁")
	}
	w, err := NewFromLegacy(email, password, m.net)
	if err != nil {
		return nil, err
	}
	w.SetNextIndex(m.loadNextIndexFor(true)) // restore persisted legacy index / 恢复持久化的传统索引
	m.unlocked = w
	return w, nil
}

// Lock drops the in-memory key material of the unlocked wallet.
// Lock 丢弃已解锁钱包的内存密钥材料。
func (m *Manager) Lock() {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.unlocked != nil {
		m.unlocked.Lock()
		m.unlocked = nil
	}
}

// Wallet returns the currently unlocked wallet, or nil when locked or absent.
// Wallet 返回当前已解锁的钱包；锁定或未创建时返回 nil。
func (m *Manager) Wallet() *Wallet {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	return m.unlocked
}

// NextAddress returns the next derived address and advances the persisted
// next-address index. The index lives in a plaintext sidecar (it is not
// sensitive) so addresses are never reused across restarts without needing the
// wallet passphrase. Legacy and BIP39 wallets keep separate sidecars.
// NextAddress 返回下一个派生地址并推进持久化的下一地址索引。索引存放于明文旁车
// 文件（非敏感），从而无需钱包口令即可避免重启后地址复用。传统与 BIP39 钱包
// 使用各自独立的旁车文件。
func (m *Manager) NextAddress() (string, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	w := m.unlocked
	if w == nil {
		return "", fmt.Errorf("wallet is locked / 钱包已锁定")
	}
	idx := w.NextIndex()
	addr, err := w.Address(idx)
	if err != nil {
		return "", err
	}
	if err := m.saveNextIndexFor(idx+1, w.legacy); err != nil {
		return "", err
	}
	w.SetNextIndex(idx + 1)
	return addr, nil
}

// metaPath returns the plaintext sidecar path storing the next address index.
// Legacy wallets use a separate ".legacy.meta" file so their index never
// collides with (and never skips index 0 because of) the BIP39 wallet.
// metaPath 返回存储下一地址索引的明文旁车文件路径。传统钱包使用独立的
// ".legacy.meta" 文件，避免与 BIP39 钱包索引冲突（也不会因此跳过 index 0）。
func (m *Manager) metaPathFor(legacy bool) string {
	if legacy {
		return m.path + ".legacy.meta"
	}
	return m.path + ".meta"
}

// loadNextIndex reads the persisted next address index (0 when absent).
// loadNextIndex 读取持久化的下一地址索引（不存在时为 0）。
func (m *Manager) loadNextIndexFor(legacy bool) uint32 {
	data, err := os.ReadFile(m.metaPathFor(legacy))
	if err != nil {
		return 0
	}
	var idx uint32
	if _, err := fmt.Sscanf(string(data), "%d", &idx); err != nil {
		return 0
	}
	return idx
}

// saveNextIndex persists the next address index.
// saveNextIndex 持久化下一地址索引。
func (m *Manager) saveNextIndexFor(i uint32, legacy bool) error {
	return os.WriteFile(m.metaPathFor(legacy), []byte(fmt.Sprintf("%d", i)), 0o600)
}

// zeroBytes clears a byte slice in place.
// zeroBytes 就地清空字节切片。
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
