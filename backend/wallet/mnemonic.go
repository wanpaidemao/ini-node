package wallet

import (
	"github.com/tyler-smith/go-bip39"
)

// WordCount is the entropy size in bits for a mnemonic.
// WordCount 为助记词对应的熵位数。
type WordCount int

const (
	// TwelveWords is a 128-bit (12-word) mnemonic.
	// TwelveWords 为 128 位（12 词）助记词。
	TwelveWords WordCount = 128

	// TwentyFourWords is a 256-bit (24-word) mnemonic.
	// TwentyFourWords 为 256 位（24 词）助记词。
	TwentyFourWords WordCount = 256
)

// GenerateMnemonic creates a new BIP39 mnemonic with the given entropy size.
// The returned phrase is the user's backup key.
// GenerateMnemonic 生成指定熵长度的 BIP39 助记词，返回的短语即用户的备份钥匙。
func GenerateMnemonic(entropyBits WordCount) (string, error) {
	entropy, err := bip39.NewEntropy(int(entropyBits))
	if err != nil {
		return "", err
	}
	return bip39.NewMnemonic(entropy)
}

// MnemonicToSeed derives the 64-byte BIP39 seed from a mnemonic and an
// optional passphrase. The same mnemonic+passphrase always yields the same
// seed, so it is used both for creation and recovery.
// MnemonicToSeed 由助记词与可选口令派生 64 字节种子；相同助记词+口令始终得到
// 相同种子，因此同时用于创建与恢复。
func MnemonicToSeed(mnemonic, passphrase string) []byte {
	return bip39.NewSeed(mnemonic, passphrase)
}

// ValidateMnemonic reports whether the mnemonic is a valid BIP39 phrase.
// ValidateMnemonic 校验助记词是否为合法 BIP39 短语。
func ValidateMnemonic(mnemonic string) bool {
	return bip39.IsMnemonicValid(mnemonic)
}
