package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type vanityMatch struct {
	ChannelID string
	Seed      []byte
	PubKey    ed25519.PublicKey
}

// mineVanity searches for channel IDs matching a pattern.
// mode: "prefix" (after TV), "contains", "suffix"
func mineVanity(ctx context.Context, pattern string, mode string, ignoreCase bool, threads int) <-chan vanityMatch {
	results := make(chan vanityMatch, 16)

	if ignoreCase {
		pattern = strings.ToLower(pattern)
	}

	matcher := func(id string) bool {
		target := id
		if ignoreCase {
			target = strings.ToLower(id)
		}
		switch mode {
		case "prefix":
			// Match after the "TV" prefix
			return strings.HasPrefix(target[2:], pattern)
		case "suffix":
			return strings.HasSuffix(target, pattern)
		case "contains":
			return strings.Contains(target, pattern)
		default:
			return strings.Contains(target, pattern)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seed := make([]byte, ed25519.SeedSize)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if _, err := rand.Read(seed); err != nil {
					continue
				}

				priv := ed25519.NewKeyFromSeed(seed)
				pub := priv.Public().(ed25519.PublicKey)
				id := makeChannelID(pub)

				if matcher(id) {
					// Copy seed since we reuse the buffer
					savedSeed := make([]byte, ed25519.SeedSize)
					copy(savedSeed, seed)
					select {
					case results <- vanityMatch{
						ChannelID: id,
						Seed:      savedSeed,
						PubKey:    pub,
					}:
					case <-ctx.Done():
						return
					}
				}

				atomic.AddUint64(&vanityChecked, 1)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}

// vanityChecked tracks total keys checked across all threads.
var vanityChecked uint64

// runVanityMiner runs the interactive vanity miner.
func runVanityMiner(pattern, mode string, ignoreCase bool, threads, maxCount int) {
	if threads <= 0 {
		threads = runtime.NumCPU()
	}

	// Validate pattern characters
	checkPattern := pattern
	if ignoreCase {
		checkPattern = strings.ToLower(pattern)
	}
	for _, ch := range checkPattern {
		valid := false
		for _, a := range b58Alphabet {
			c := a
			if ignoreCase {
				// Check if lowercased version matches
				lc := strings.ToLower(string(a))
				if lc == string(ch) {
					valid = true
					break
				}
			} else if rune(c) == ch {
				valid = true
				break
			}
		}
		if !valid {
			fatal("pattern contains character %q not achievable in base58", string(ch))
		}
	}

	modeLabel := mode
	if ignoreCase {
		modeLabel += ", case-insensitive"
	}

	fmt.Printf("Mining for %q (%s, %d threads)...\n\n", pattern, modeLabel, threads)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl-C
	go func() {
		sigCh := make(chan os.Signal, 1)
		signalNotify(sigCh)
		<-sigCh
		cancel()
	}()

	start := time.Now()
	atomic.StoreUint64(&vanityChecked, 0)

	results := mineVanity(ctx, pattern, mode, ignoreCase, threads)

	// Progress ticker
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	found := 0
	done := false

	for !done {
		select {
		case match, ok := <-results:
			if !ok {
				done = true
				break
			}

			elapsed := time.Since(start)
			checked := atomic.LoadUint64(&vanityChecked)

			// Save key file
			filename := match.ChannelID + ".key"
			if err := os.WriteFile(filename, match.Seed, 0600); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not save %s: %v\n", filename, err)
			}

			if useColor {
				fmt.Printf("  %s%s%s  %.1fs  %s keys\n", cGreen, match.ChannelID, cReset, elapsed.Seconds(), formatCount(checked))
				fmt.Printf("  %sSaved%s %s\n\n", cDim, cReset, filename)
			} else {
				fmt.Printf("  %s  %.1fs  %s keys\n", match.ChannelID, elapsed.Seconds(), formatCount(checked))
				fmt.Printf("  Saved %s\n\n", filename)
			}

			found++
			if maxCount > 0 && found >= maxCount {
				cancel()
				done = true
			}

		case <-ticker.C:
			elapsed := time.Since(start)
			checked := atomic.LoadUint64(&vanityChecked)
			rate := float64(checked) / elapsed.Seconds()
			if useColor {
				fmt.Printf("\r  %s[%s keys, %.0f/s]%s", cDim, formatCount(checked), rate, cReset)
			}

		case <-ctx.Done():
			done = true
		}
	}

	elapsed := time.Since(start)
	checked := atomic.LoadUint64(&vanityChecked)
	rate := float64(checked) / elapsed.Seconds()

	fmt.Printf("\n%d match(es) / %s keys / %.1fs / %.0f keys/s\n",
		found, formatCount(checked), elapsed.Seconds(), rate)
}

// formatCount formats a large number with K/M/B suffixes.
func formatCount(n uint64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}
