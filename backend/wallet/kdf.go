// Legacy KDF: replicates the original web-wallet regular-wallet key
// derivation (index.html:1442-1453) for backward-compatible imports. It is
// the foundation of the email/password login (see the built-in HD wallet plan).
// 传统 KDF：复刻原 web-wallet 常规钱包密钥派生 (index.html:1442-1453)，用于向后
// 兼容导入；是邮箱密码登录的基础（见内置 HD 钱包方案）。
package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// emailRe matches a lowercase email (mirrors the JS email check).
// emailRe 匹配小写邮箱（对应 JS 邮箱检查）。
var emailRe = regexp.MustCompile(`[\s\w\d]+@[\s\w\d]+`)

// lowerCharRe, lowerUpRe and digitRe match character-group runs used by the
// legacy KDF's regex match-group counting.
// lowerCharRe、lowerUpRe、digitRe 匹配传统 KDF 用于正则匹配组计数的字符组。
var (
	lowerCharRe = regexp.MustCompile(`[a-z]+`)
	lowerUpRe   = regexp.MustCompile(`[A-Z]+`)
	digitRe     = regexp.MustCompile(`[0-9]+`)
)

// countMatchGroups returns the number of regex match groups, or 1 if no match.
// countMatchGroups 返回正则匹配组数，无匹配则返回 1。
// JS: pass.match(/[a-z]+/g) ? .length : 1
func countMatchGroups(re *regexp.Regexp, s string) int {
	matches := re.FindAllString(s, -1)
	if matches == nil {
		return 1
	}
	return len(matches)
}

// jsLength returns the number of UTF-16 code units in a string, matching
// JavaScript's String.prototype.length. / jsLength 返回字符串的 UTF-16 码元数，
// 与 JavaScript 的 String.prototype.length 一致。
//
// Go's len(string) returns UTF-8 byte count; JS .length returns UTF-16 code
// unit count. For BMP characters (code point <= 0xFFFF) they agree when the
// string is pure ASCII, but diverge for non-ASCII BMP chars (e.g. Chinese:
// 1 rune = 1 UTF-16 unit = 3 UTF-8 bytes) and for astral chars (emoji:
// 1 rune = 2 UTF-16 units = 4 UTF-8 bytes). The original web-wallet KDF uses
// JS .length, so we must match it exactly or the derived seed diverges.
// Go 的 len(string) 返回 UTF-8 字节数；JS .length 返回 UTF-16 码元数。
// 对于 BMP 字符（码点 <= 0xFFFF），纯 ASCII 时两者一致；但对非 ASCII BMP 字符
// （如中文：1 rune = 1 UTF-16 码元 = 3 UTF-8 字节）和星平面字符（emoji：
// 1 rune = 2 UTF-16 码元 = 4 UTF-8 字节）会不一致。原 web-wallet KDF 用 JS .length，
// 必须完全对齐，否则派生种子会分叉。
func jsLength(s string) int {
	count := 0
	for _, r := range s {
		if r > 0xFFFF {
			count += 2 // astral plane: surrogate pair / 星平面：代理对
		} else {
			count += 1
		}
	}
	return count
}

// LegacyRegularSeed replicates the original KDF to derive a 32-byte seed.
// LegacyRegularSeed 复刻原始 KDF 派生 32 字节种子。
//
// Algorithm (index.html:1442-1453):
// 算法 (index.html:1442-1453)：
//
//	email = email.toLowerCase()
//	s = email
//	s += '|' + pass + '|'
//	s += s.length + '|!@' + ((pass.length * 7) + email.length) * 7
//	regchars  = match(/[a-z]+/g).length  or 1
//	regupchars= match(/[A-Z]+/g).length  or 1
//	regnums   = match(/[0-9]+/g).length  or 1
//	s += ((regnums + regchars) + regupchars) * pass.length + '3571'
//	s += (s + '' + s)   // s = s + s + s (tripled)
//	for i:=0; i<=51; i++ { s = sha256(s).hex() }
//	seed = hex.Decode(s)
//
// NOTE: all .length calls use JS UTF-16 code unit counts (see jsLength).
// 注意：所有 .length 调用使用 JS UTF-16 码元数（见 jsLength）。
func LegacyRegularSeed(email, password string) ([]byte, error) {
	if email == "" || password == "" {
		return nil, errors.New("email and password required / 需要邮箱和口令")
	}
	email = toLower(email) // JS: email.toLowerCase() / JS: email.toLowerCase()
	if !emailRe.MatchString(email) {
		return nil, errors.New("invalid email / 邮箱格式无效")
	}
	// JS checks pass.length >= 10 (UTF-16 units). / JS 校验 pass.length >= 10（UTF-16 码元）。
	if jsLength(password) < 10 {
		return nil, errors.New("password too short (min 10) / 口令过短（至少10位）")
	}

	pass := password
	// s = email / s = email
	s := email
	// s += '|' + pass + '|' / s += '|' + pass + '|'
	s += "|" + pass + "|"
	// s += s.length + '|!@' + ((pass.length * 7) + email.length) * 7
	// Note: s.length here is the length of s BEFORE this append (JS UTF-16 units).
	// 注意：此处 s.length 是追加前 s 的长度（JS UTF-16 码元）。
	sLen := jsLength(s)
	s += strconv.Itoa(sLen) + "|!@" + strconv.Itoa(((jsLength(pass)*7)+jsLength(email))*7)
	// regchars / regupchars / regnums (match group counts, default 1).
	// regchars / regupchars / regnums（匹配组数，默认1）。
	regchars := countMatchGroups(lowerCharRe, pass)
	regupchars := countMatchGroups(lowerUpRe, pass)
	regnums := countMatchGroups(digitRe, pass)
	// s += ((regnums + regchars) + regupchars) * pass.length + '3571'
	s += strconv.Itoa(((regnums+regchars)+regupchars)*jsLength(pass)) + "3571"
	// s += (s + '' + s)  →  s = s + s + s (tripled).
	// s += (s + '' + s)  →  s = s + s + s（三倍拼接）。
	s = s + s + s

	// 52 rounds of SHA-256. / 52 轮 SHA-256。
	for i := 0; i <= 51; i++ {
		sum := sha256.Sum256([]byte(s))
		s = hex.EncodeToString(sum[:])
	}

	seed, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode seed hex / 解码种子十六进制: %w", err)
	}
	return seed, nil
}

// toLower is a plain ASCII toLower (email is ASCII-safe).
// toLower 是纯 ASCII 小写转换（邮箱为 ASCII 安全）。
func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
