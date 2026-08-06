package jwttoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// Secret 与 node 服务器 node/internal/auth 中一致的签名密钥
const Secret = "due-game-server-secret-change-me"

type claims struct {
	UID  int64  `json:"uid"`
	Type string `json:"type"`
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
}

// GenerateAccessToken 生成 access 令牌，与服务器 auth.GenerateToken 产出格式一致
func GenerateAccessToken(uid int64, ttl time.Duration) (string, error) {
	now := time.Now().Unix()
	return sign(&claims{UID: uid, Type: "access", Iat: now, Exp: now + int64(ttl.Seconds())})
}

func sign(c *claims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := base64.RawURLEncoding.EncodeToString(signBytes(header + "." + payload))
	return header + "." + payload + "." + sig, nil
}

func signBytes(data string) []byte {
	h := hmac.New(sha256.New, []byte(Secret))
	h.Write([]byte(data))
	return h.Sum(nil)
}
