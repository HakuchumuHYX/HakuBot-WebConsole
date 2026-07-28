package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
)

const cursorPayloadSize = 16

type CursorCodec struct {
	secret []byte
}

func NewCursorCodec(secret []byte) CursorCodec {
	return CursorCodec{secret: append([]byte(nil), secret...)}
}

func (c CursorCodec) Encode(startedAtMS, id int64) string {
	payload := make([]byte, cursorPayloadSize)
	binary.BigEndian.PutUint64(payload[0:8], uint64(startedAtMS))
	binary.BigEndian.PutUint64(payload[8:16], uint64(id))
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(payload)
	token := append(payload, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(token)
}

func (c CursorCodec) Decode(value string) (int64, int64, error) {
	token, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(token) != cursorPayloadSize+sha256.Size {
		return 0, 0, errors.New("invalid cursor")
	}
	payload := token[:cursorPayloadSize]
	signature := token[cursorPayloadSize:]
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return 0, 0, errors.New("invalid cursor")
	}
	startedAtMS := int64(binary.BigEndian.Uint64(payload[0:8]))
	id := int64(binary.BigEndian.Uint64(payload[8:16]))
	if startedAtMS < 0 || id <= 0 {
		return 0, 0, errors.New("invalid cursor")
	}
	return startedAtMS, id, nil
}
