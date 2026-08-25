package storage

import (
	"context"
	"testing"
	"time"
)

func TestRestartAttemptsEnforceLimitAndStableReset(t *testing.T) {
	database := openTestDatabase(t)
	id := seedRuntimeInstance(t, database)
	repository, _ := NewRestartAttemptRepository(database)
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	attempt, allowed, err := repository.Claim(context.Background(), id, now, time.Minute, 3)
	if err != nil || !allowed || attempt != 1 {
		t.Fatalf("initial Claim() = (%d,%t,%v)", attempt, allowed, err)
	}
	if err := repository.ReleaseClaim(context.Background(), id, attempt); err != nil {
		t.Fatal(err)
	}
	for expected := 1; expected <= 3; expected++ {
		attempt, allowed, err := repository.Claim(context.Background(), id, now.Add(time.Duration(expected)*time.Second), time.Minute, 3)
		if err != nil || !allowed || attempt != expected {
			t.Fatalf("Claim(%d) = (%d,%t,%v)", expected, attempt, allowed, err)
		}
	}
	if attempt, allowed, err := repository.Claim(context.Background(), id, now.Add(4*time.Second), time.Minute, 3); err != nil || allowed || attempt != 4 {
		t.Fatalf("limited Claim() = (%d,%t,%v)", attempt, allowed, err)
	}
	if err := repository.MarkReady(context.Background(), id, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkReady(context.Background(), id, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	attempt, allowed, err = repository.Claim(context.Background(), id, now.Add(66*time.Second), time.Minute, 3)
	if err != nil || !allowed || attempt != 1 {
		t.Fatalf("stable Claim() = (%d,%t,%v)", attempt, allowed, err)
	}
}
