package domain

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

func NewID(prefix string, now time.Time) string {
	entropy := ulid.Monotonic(rand.Reader, 0)
	return fmt.Sprintf("%s_%s", prefix, ulid.MustNew(ulid.Timestamp(now), entropy).String())
}
