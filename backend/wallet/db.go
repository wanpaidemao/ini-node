package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"golang.org/x/crypto/scrypt"
)

// Store file constants. / 存储文件常量。
const (
	// storeVersion is the on-disk format version.
	// storeVersion 为磁盘格式版本。
	storeVersion = 1

	// scryptN/R/P are the scrypt KDF parameters for passphrase → key derivation.
	// scryptN/R/P 为 scrypt KDF 参数（口令 → 密钥派生）。
	scryptN = 1 << 15 // 32768, CPU/memory cost / CPU/内存开销
	scryptR = 8
	scryptP = 1

	// keyLen is the derived key length in bytes (AES-256).
	// keyLen 为派生密钥长度（AES-256）。
	keyLen = 32

	// saltLen is the random salt length for scrypt.
	// saltLen 为 scrypt 随机盐长度。
	saltLen = 16

	// nonceLen is the AES-GCM nonce length.
	// nonceLen 为 AES-GCM 随机数长度。
	nonceLen = 12
)

// storedWallet is the on-disk encrypted representation of a wallet.
// storedWallet 为钱包的磁盘加密表示。
type storedWallet struct {
	Version   uint32 `json:"version"`   // format version / 格式版本
	Network   string `json:"network"`   // network name / 网络名称
	CoinType  uint32 `json:"coinType"`  // hardened coin type / 币种类型
	Account   uint32 `json:"account"`   // hardened account / 账户
	NextIndex uint32 `json:"nextIndex"` // next address index / 下一地址索引
	Salt      []byte `json:"salt"`      // scrypt salt / scrypt 盐
	Nonce     []byte `json:"nonce"`     // AES-GCM nonce / AES-GCM 随机数
	Verifier  []byte `json:"verifier"`  // sha256(key), passphrase check / 口令校验值
	Cipher    []byte `json:"cipher"`    // AES-GCM(seed), incl. auth tag / 密文（含认证标签）
}

// DefaultWalletDir is the wallet directory name under the data directory,
// holding one <name>.db file per BIP39 wallet.
// DefaultWalletDir 为数据目录下的钱包目录名，每个 BIP39 钱包一个 <name>.db 文件。
const DefaultWalletDir = "wallet"

// SaveWallet encrypts the wallet seed with a passphrase-derived key and writes
// it to path (e.g. <datadir>/wallet.db). The file is written atomically via a
// temp file and locked down to 0600.
// SaveWallet 用口令派生密钥加密钱包种子并写入 path（如 <datadir>/wallet.db）。
// 通过临时文件原子写入，权限收紧为 0600。
func (w *Wallet) SaveWallet(path, passphrase string) error {
	if w.seed == nil {
		return fmt.Errorf("wallet is not unlocked / 钱包未解锁")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("salt / 盐: %w", err)
	}
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return fmt.Errorf("scrypt key derivation / 密钥派生: %w", err)
	}
	// verifier = sha256(key) enables a constant-time passphrase check on unlock.
	// 校验值 = sha256(key)，用于解锁时的恒定时间口令校验。
	verifier := sha256.Sum256(key)

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("nonce / 随机数: %w", err)
	}
	ciphertext, err := aesGCMSeal(key, nonce, w.seed)
	if err != nil {
		return fmt.Errorf("encrypt seed / 加密种子: %w", err)
	}
	sw := storedWallet{
		Version:   storeVersion,
		Network:   w.net.Name,
		CoinType:  w.coinType,
		Account:   w.account,
		NextIndex: w.nextIndex,
		Salt:      salt,
		Nonce:     nonce,
		Verifier:  verifier[:],
		Cipher:    ciphertext,
	}
	data, err := json.MarshalIndent(sw, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UnlockWallet loads and decrypts the wallet at path with the passphrase,
// rebuilding the HD master key. A wrong passphrase returns an error.
// UnlockWallet 用口令加载并解密 path 处的钱包，重建 HD 主密钥；口令错误返回错误。
func UnlockWallet(path, passphrase string, net *chaincfg.Params) (*Wallet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sw storedWallet
	if err := json.Unmarshal(data, &sw); err != nil {
		return nil, fmt.Errorf("parse wallet file / 解析钱包文件: %w", err)
	}
	if sw.Version != storeVersion {
		return nil, fmt.Errorf("unsupported wallet version %d (不支持的存储版本 %d)", sw.Version, sw.Version)
	}
	if sw.Network != "" && sw.Network != net.Name {
		return nil, fmt.Errorf("wallet network %q does not match %q (钱包网络 %q 与 %q 不匹配)",
			sw.Network, net.Name, sw.Network, net.Name)
	}
	key, err := scrypt.Key([]byte(passphrase), sw.Salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return nil, err
	}
	// Constant-time passphrase check before attempting decryption.
	// 解密前进行恒定时间口令校验。
	verifier := sha256.Sum256(key)
	if subtle.ConstantTimeCompare(verifier[:], sw.Verifier) != 1 {
		return nil, fmt.Errorf("incorrect wallet passphrase / 钱包口令错误")
	}
	seed, err := aesGCMOpen(key, sw.Nonce, sw.Cipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt seed / 解密种子: %w", err)
	}
	w, err := NewFromSeedPath(seed, net, sw.CoinType, sw.Account)
	// clear the temporary seed copy regardless of outcome / 无论成败都清空临时种子副本
	for i := range seed {
		seed[i] = 0
	}
	if err != nil {
		return nil, err
	}
	w.nextIndex = sw.NextIndex
	return w, nil
}

// aesGCMSeal encrypts plaintext with AES-GCM using the 32-byte key.
// aesGCMSeal 用 AES-GCM 和 32 字节密钥加密明文。
func aesGCMSeal(key, nonce, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, nil), nil
}

// aesGCMOpen decrypts an AES-GCM ciphertext, authenticating with the nonce.
// aesGCMOpen 用 AES-GCM 解密密文，并以随机数完成认证。
func aesGCMOpen(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}
