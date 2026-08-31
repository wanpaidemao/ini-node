package wallet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/btcsuite/btcd/chaincfg/v2"
)

// Manager owns the built-in wallet lifecycle: it tracks the wallet directory
// (holding one <name>.db file per BIP39 wallet), the network parameters, and
// the currently unlocked wallet. It is safe for concurrent use.
// Manager 管理内置钱包生命周期：记录钱包目录（每个 BIP39 钱包一个 <name>.db
// 文件）、网络参数与当前已解锁钱包。支持并发访问。
type Manager struct {
	mtx      sync.Mutex       // guards the fields below / 保护以下字段
	dir      string           // wallet directory / 钱包目录
	net      *chaincfg.Params // network parameters / 网络参数
	unlocked *Wallet          // unlocked wallet, nil if locked / 已解锁钱包（锁定时为 nil）
	name     string           // currently unlocked wallet name / 当前解锁钱包名
}

// NewManager creates a wallet manager rooted at dir (a directory holding one
// <name>.db file per wallet). A legacy single-file wallet.db at dir's parent is
// migrated into <dir>/default.db on first use, preserving existing funds.
// NewManager 创建以 dir 为钱包目录（每个钱包一个 <name>.db 文件）的管理器。
// 首次使用时把 dir 父目录下的旧单文件 wallet.db 迁移为 <dir>/default.db，
// 保留既有资金。
func NewManager(dir string, net *chaincfg.Params) *Manager {
	m := &Manager{dir: dir, net: net}
	m.migrateLegacy()
	return m
}

// migrateLegacy moves a legacy <datadir>/wallet.db (and its index sidecars)
// into the new <datadir>/wallet/default.db layout.
// migrateLegacy 把旧的 <datadir>/wallet.db（及其索引旁车）迁移为
// <datadir>/wallet/default.db。
func (m *Manager) migrateLegacy() {
	old := filepath.Join(filepath.Dir(m.dir), "wallet.db")
	if _, err := os.Stat(old); err != nil {
		return
	}
	newPath := m.walletPath("default")
	if _, err := os.Stat(newPath); err == nil {
		return // already migrated / 已迁移
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return
	}
	if err := os.Rename(old, newPath); err != nil {
		return
	}
	// Migrate the index sidecars too (best-effort). / 一并迁移索引旁车（尽力而为）。
	if _, err := os.Stat(old + ".meta"); err == nil {
		_ = os.Rename(old+".meta", newPath+".meta")
	}
	if _, err := os.Stat(old + ".legacy.meta"); err == nil {
		_ = os.Rename(old+".legacy.meta", filepath.Join(m.dir, "legacy.meta"))
	}
	if _, err := os.Stat(old + ".wif.meta"); err == nil {
		_ = os.Rename(old+".wif.meta", filepath.Join(m.dir, "wif.meta"))
	}
}

// walletPath returns the on-disk path of the wallet named name.
// walletPath 返回名为 name 的钱包磁盘路径。
func (m *Manager) walletPath(name string) string {
	return filepath.Join(m.dir, name+".db")
}

// normalizeName falls back to the default wallet name when name is blank.
// normalizeName 在 name 为空时回退到默认钱包名。
func (m *Manager) normalizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	return name
}

// List returns the names of all wallets in the directory, sorted.
// List 返回目录下所有钱包名（排序）。
func (m *Manager) List() ([]string, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".db") || strings.HasSuffix(n, ".meta") {
			continue
		}
		names = append(names, strings.TrimSuffix(n, ".db"))
	}
	sort.Strings(names)
	return names, nil
}

// Exists reports whether the named wallet file is present on disk.
// Exists 报告磁盘上是否已存在指定名称的钱包文件。
func (m *Manager) Exists(name string) bool {
	_, err := os.Stat(m.walletPath(name))
	return err == nil
}

// Name returns the currently unlocked wallet name ("" when locked).
// Name 返回当前解锁钱包名（锁定时为 ""）。
func (m *Manager) Name() string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	return m.name
}

// Create generates a new HD wallet from a fresh mnemonic, saves it encrypted
// at <dir>/<name>.db and leaves it unlocked. It returns the mnemonic once so
// the caller can present it for backup. Errors when the wallet already exists
// or one is already unlocked.
// Create 用全新助记词生成 HD 钱包，以口令加密保存到 <dir>/<name>.db 并保持
// 解锁；返回助记词供备份展示。钱包已存在或已有解锁钱包时报错。
func (m *Manager) Create(name, passphrase string) (mnemonic string, w *Wallet, err error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	name = m.normalizeName(name)
	if m.unlocked != nil {
		return "", nil, fmt.Errorf("wallet is already unlocked / 钱包已解锁")
	}
	if m.Exists(name) {
		return "", nil, fmt.Errorf("wallet already exists: %s / 钱包已存在: %s", name, name)
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
	if err := w.SaveWallet(m.walletPath(name), passphrase); err != nil {
		return "", nil, err
	}
	m.name = name
	_ = m.saveNextIndexFor(0, "") // ensure the sidecar exists / 确保旁车文件存在
	m.unlocked = w
	return mnemonic, w, nil
}

// Restore rebuilds a wallet from a BIP39 mnemonic, saves it encrypted at
// <dir>/<name>.db and leaves it unlocked. It errors when the wallet name is
// already taken or the mnemonic is invalid.
// Restore 用 BIP39 助记词重建钱包，以口令加密保存到 <dir>/<name>.db 并保持
// 解锁。钱包名已占用或助记词无效时报错。
func (m *Manager) Restore(name, mnemonic, passphrase string) (*Wallet, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	name = m.normalizeName(name)
	if m.unlocked != nil {
		return nil, fmt.Errorf("wallet is already unlocked / 钱包已解锁")
	}
	if m.Exists(name) {
		return nil, fmt.Errorf("wallet already exists: %s / 钱包已存在: %s", name, name)
	}
	mnemonic = strings.TrimSpace(mnemonic)
	if !ValidateMnemonic(mnemonic) {
		return nil, fmt.Errorf("invalid mnemonic / 助记词无效")
	}
	seed := MnemonicToSeed(mnemonic, "")
	w, err := NewFromSeed(seed, m.net)
	zeroBytes(seed) // clear the temporary seed copy / 清空临时种子副本
	if err != nil {
		return nil, err
	}
	if err := w.SaveWallet(m.walletPath(name), passphrase); err != nil {
		return nil, err
	}
	m.name = name
	_ = m.saveNextIndexFor(0, "") // ensure the sidecar exists / 确保旁车文件存在
	m.unlocked = w
	return w, nil
}

// Unlock loads and decrypts the named wallet file with passphrase, leaving it
// unlocked. A wrong passphrase returns an error.
// Unlock 用口令加载并解密指定钱包文件并保持解锁；口令错误返回错误。
func (m *Manager) Unlock(name, passphrase string) (*Wallet, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.unlocked != nil {
		return m.unlocked, nil
	}
	name = m.normalizeName(name)
	w, err := UnlockWallet(m.walletPath(name), passphrase, m.net)
	if err != nil {
		return nil, err
	}
	m.name = name
	w.SetNextIndex(m.loadNextIndexFor("")) // restore persisted address index / 恢复持久化的地址索引
	m.unlocked = w
	return w, nil
}

// Login derives a wallet from the legacy email/password KDF purely in memory
// and leaves it unlocked. Unlike Create/Unlock/Restore it never touches disk,
// so legacy login is fully isolated from the BIP39 wallets (no cross-overwrite).
// Its next-address index lives in a separate sidecar, so index 0 (the original
// web-wallet address) is always the first address handed out.
// Login 用传统邮箱密码 KDF 纯内存派生钱包并保持解锁。与 Create/Unlock/Restore
// 不同，它不落盘，与 BIP39 钱包完全隔离（互不覆盖）。其下一地址索引存放于独立
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
	m.name = "legacy"
	w.SetNextIndex(m.loadNextIndexFor(".legacy")) // restore persisted legacy index / 恢复持久化的传统索引
	m.unlocked = w
	return w, nil
}

// Lock drops the in-memory key material of the unlocked wallet. The wallet
// name is kept so the frontend still knows which wallet this session was
// using (so a re-unlock targets the right <name>.db).
// Lock 丢弃已解锁钱包的内存密钥材料。钱包名予以保留，前端仍能知道本会话
// 使用的是哪个钱包（重新解锁时能命中正确的 <name>.db）。
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
// wallet passphrase. Legacy, WIF and BIP39 wallets keep separate sidecars.
// NextAddress 返回下一个派生地址并推进持久化的下一地址索引。索引存放于明文旁车
// 文件（非敏感），从而无需钱包口令即可避免重启后地址复用。传统、WIF 与 BIP39
// 钱包使用各自独立的旁车文件。
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
	if err := m.saveNextIndexFor(idx+1, w.sidecarSuffix()); err != nil {
		return "", err
	}
	w.SetNextIndex(idx + 1)
	return addr, nil
}

// LoginWIF derives a single-key wallet from an imported WIF private key
// purely in memory (hybrid mode, see NewFromWIF) and leaves it unlocked.
// Like Login it never touches disk; its next-address index lives in a
// dedicated ".wif" sidecar, so the imported key's address (index 0) is
// always the first address handed out.
// LoginWIF 用导入的 WIF 私钥纯内存派生单钥钱包（混合模式，见 NewFromWIF）
// 并保持解锁。与 Login 一样不落盘；其下一地址索引存于独立的 ".wif" 旁车
// 文件，导入私钥的地址（index 0）始终第一个分配。
func (m *Manager) LoginWIF(wifStr string) (*Wallet, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.unlocked != nil {
		return nil, fmt.Errorf("wallet is already unlocked / 钱包已解锁")
	}
	w, err := NewFromWIF(wifStr, m.net)
	if err != nil {
		return nil, err
	}
	m.name = "wif"
	w.SetNextIndex(m.loadNextIndexFor(".wif")) // restore persisted WIF index / 恢复持久化的 WIF 索引
	m.unlocked = w
	return w, nil
}

// metaPathFor returns the plaintext sidecar path storing the next address
// index. The BIP39 wallet sidecar follows the wallet name; legacy and WIF
// use fixed names so each kind never collides (and each hands out its own
// index 0 first).
// metaPathFor 返回存储下一地址索引的明文旁车文件路径。BIP39 钱包旁车跟随
// 钱包名；传统与 WIF 使用固定名，类别间互不冲突（各自都从自己的 index 0
// 起分配）。
func (m *Manager) metaPathFor(suffix string) string {
	switch suffix {
	case ".legacy":
		return filepath.Join(m.dir, "legacy.meta")
	case ".wif":
		return filepath.Join(m.dir, "wif.meta")
	default:
		return m.walletPath(m.name) + ".meta"
	}
}

// loadNextIndexFor reads the persisted next address index (0 when absent).
// loadNextIndexFor 读取持久化的下一地址索引（不存在时为 0）。
func (m *Manager) loadNextIndexFor(suffix string) uint32 {
	data, err := os.ReadFile(m.metaPathFor(suffix))
	if err != nil {
		return 0
	}
	var idx uint32
	if _, err := fmt.Sscanf(string(data), "%d", &idx); err != nil {
		return 0
	}
	return idx
}

// saveNextIndexFor persists the next address index.
// saveNextIndexFor 持久化下一地址索引。
func (m *Manager) saveNextIndexFor(i uint32, suffix string) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.metaPathFor(suffix), []byte(fmt.Sprintf("%d", i)), 0o600)
}

// zeroBytes clears a byte slice in place.
// zeroBytes 就地清空字节切片。
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}