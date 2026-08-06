package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claims JWT 载荷
type Claims struct {
	UID  int64  `json:"uid"`
	Type string `json:"type"` // access / refresh
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
}

var (
	secret          string
	accessTokenTTL  = 2 * time.Hour
	refreshTokenTTL = 7 * 24 * time.Hour
)

// Init 初始化认证配置
func Init(sec string, accessTTL, refreshTTL time.Duration) {
	secret = sec
	if accessTTL > 0 {
		accessTokenTTL = accessTTL
	}
	if refreshTTL > 0 {
		refreshTokenTTL = refreshTTL
	}
}

// GenerateToken 生成一对令牌（访问令牌 + 刷新令牌）
func GenerateToken(uid int64) (accessToken, refreshToken string, err error) {
	now := time.Now().Unix()

	accessClaims := &Claims{UID: uid, Type: "access", Iat: now, Exp: now + int64(accessTokenTTL.Seconds())}
	accessToken, err = sign(accessClaims)
	if err != nil {
		return "", "", fmt.Errorf("sign access token: %w", err)
	}

	refreshClaims := &Claims{UID: uid, Type: "refresh", Iat: now, Exp: now + int64(refreshTokenTTL.Seconds())}
	refreshToken, err = sign(refreshClaims)
	if err != nil {
		return "", "", fmt.Errorf("sign refresh token: %w", err)
	}

	return
}

// ParseToken 解析并验证令牌，成功返回 Claims，失败返回 error
func ParseToken(tokenString string) (*Claims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	// 验证签名
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}

	expectedSig := signBytes(parts[0] + "." + parts[1])
	if !hmac.Equal(sig, expectedSig) {
		return nil, fmt.Errorf("invalid signature")
	}

	// 解码 payload
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding: %w", err)
	}

	claims := &Claims{}
	if err := json.Unmarshal(payload, claims); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// 检查过期
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}

	return claims, nil
}

func sign(claims *Claims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := base64.RawURLEncoding.EncodeToString(signBytes(header + "." + payload))
	return header + "." + payload + "." + sig, nil
}

func signBytes(data string) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return h.Sum(nil)
}
