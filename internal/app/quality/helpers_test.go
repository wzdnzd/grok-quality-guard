package quality

import (
	"testing"
	"time"

	"grok-quality-guard/internal/config"
)

func TestEmptyStreamUsesFifteenMinuteCooldown(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	if got := cooldownForClass(ClassEmptyStream, cfg); got != 15*time.Minute {
		t.Fatalf("empty stream cooldown = %s", got)
	}
}
