package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/sha3"
)

var (
	screenResolutions = [][2]int{{1920, 1080}, {1440, 900}, {2560, 1440}, {3840, 2160}}
	documentKeys      = []string{
		"__reactContainer$fzelfjyxej8", "_reactListening5dehydibo78", "location",
	}
	coreCounts = []int{8, 16, 24, 32}

	navigatorKeys = []string{
		"registerProtocolHandler−function registerProtocolHandler() { [native code] }",
		"storage−[object StorageManager]",
		"locks−[object LockManager]",
		"appCodeName−Mozilla",
		"permissions−[object Permissions]",
		"share−function share() { [native code] }",
		"webdriver−false",
		"managed−[object NavigatorManagedData]",
		"canShare−function canShare() { [native code] }",
		"vendor−Google Inc.",
		"mediaDevices−[object MediaDevices]",
		"vibrate−function vibrate() { [native code] }",
		"storageBuckets−[object StorageBucketManager]",
		"mediaCapabilities−[object MediaCapabilities]",
		"cookieEnabled−true",
		"virtualKeyboard−[object VirtualKeyboard]",
		"product−Gecko",
		"presentation−[object Presentation]",
		"onLine−true",
		"mimeTypes−[object MimeTypeArray]",
		"credentials−[object CredentialsContainer]",
		"serviceWorker−[object ServiceWorkerContainer]",
		"keyboard−[object Keyboard]",
		"gpu−[object GPU]",
		"doNotTrack",
		"serial−[object Serial]",
		"pdfViewerEnabled−true",
		"language−zh-CN",
		"geolocation−[object Geolocation]",
		"userAgentData−[object NavigatorUAData]",
		"getUserMedia−function getUserMedia() { [native code] }",
		"sendBeacon−function sendBeacon() { [native code] }",
		"hardwareConcurrency−32",
		"windowControlsOverlay−[object WindowControlsOverlay]",
	}

	windowKeys = []string{
		"0", "window", "self", "document", "name", "location", "customElements",
		"history", "navigation", "innerWidth", "innerHeight", "scrollX", "scrollY",
		"visualViewport", "screenX", "screenY", "outerWidth", "outerHeight",
		"devicePixelRatio", "screen", "chrome", "navigator", "onresize", "performance",
		"crypto", "indexedDB", "sessionStorage", "localStorage", "scheduler", "alert",
		"atob", "btoa", "fetch", "matchMedia", "postMessage", "queueMicrotask",
		"requestAnimationFrame", "setInterval", "setTimeout", "caches", "__NEXT_DATA__",
		"__BUILD_MANIFEST", "__NEXT_PRELOADREADY",
	}
)

func cryptoRandInt(n int) int {
	bi, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(bi.Int64())
}

func legacyParseTime() string {
	loc := time.FixedZone("EST", -5*60*60)
	now := time.Now().In(loc)
	return now.Format("Mon Jan 02 2006 15:04:05") + " GMT-0500 (Eastern Standard Time)"
}

func buildPowConfig(userAgent string, scriptSources []string, dataBuild string) []interface{} {
	scrIdx := cryptoRandInt(len(screenResolutions))
	chosen := screenResolutions[scrIdx]
	screen := chosen[0] + chosen[1]

	source := defaultPowScript
	if len(scriptSources) > 0 {
		source = scriptSources[cryptoRandInt(len(scriptSources))]
	}

	navKey := navigatorKeys[cryptoRandInt(len(navigatorKeys))]
	docKey := documentKeys[cryptoRandInt(len(documentKeys))]
	winKey := windowKeys[cryptoRandInt(len(windowKeys))]
	cores := coreCounts[cryptoRandInt(len(coreCounts))]

	now := time.Now()
	perfMs := float64(now.UnixMilli())
	timeOrigin := 0.0

	return []interface{}{
		screen,
		legacyParseTime(),
		4294705152,
		1,
		userAgent,
		source,
		dataBuild,
		"en-US",
		"en-US,es-US,en,es",
		float64(cryptoRandInt(1000000)) / 1000000.0,
		navKey,
		docKey,
		winKey,
		perfMs,
		uuid.New().String(),
		"",
		cores,
		timeOrigin,
		0, 0, 0, 0, 0, 0,
		0,
	}
}

func BuildLegacyRequirementsToken(userAgent string, scriptSources []string, dataBuild string) string {
	cfg := buildPowConfig(userAgent, scriptSources, dataBuild)
	data, _ := json.Marshal(cfg)
	encoded := base64.StdEncoding.EncodeToString(data)
	return "gAAAAAC" + encoded
}

func BuildProofToken(seed, difficulty string, userAgent string, scriptSources []string, dataBuild string) (string, error) {
	cfg := buildPowConfig(userAgent, scriptSources, dataBuild)
	answer, err := powGenerate(seed, difficulty, cfg, 500000)
	if err != nil {
		return "", err
	}
	return "gAAAAAB" + answer, nil
}

func powGenerate(seed, difficulty string, config []interface{}, limit int) (string, error) {
	target, err := hex.DecodeString(difficulty)
	if err != nil {
		return "", fmt.Errorf("invalid difficulty: %w", err)
	}
	diffLen := len(difficulty) / 2

	part1JSON, _ := json.Marshal(config[:3])
	part1JSON = part1JSON[:len(part1JSON)-1]
	s1 := string(part1JSON) + ","

	part2JSON, _ := json.Marshal(config[4:9])
	s2 := "," + string(part2JSON[1:len(part2JSON)-1]) + ","

	part3JSON, _ := json.Marshal(config[10:])
	s3 := "," + string(part3JSON[1:])

	seedBytes := []byte(seed)

	var sb strings.Builder
	sb.Grow(len(s1) + 12 + len(s2) + 12 + len(s3))

	for i := 0; i < limit; i++ {
		sb.Reset()
		sb.WriteString(s1)
		sb.WriteString(fmt.Sprintf("%d", i))
		sb.WriteString(s2)
		sb.WriteString(fmt.Sprintf("%d", i>>1))
		sb.WriteString(s3)

		finalStr := sb.String()
		encoded := base64.StdEncoding.EncodeToString([]byte(finalStr))

		hashInput := make([]byte, len(seedBytes)+len(encoded))
		copy(hashInput, seedBytes)
		copy(hashInput[len(seedBytes):], encoded)

		digest := sha3.Sum512(hashInput)

		solved := true
		for j := 0; j < diffLen; j++ {
			if digest[j] < target[j] {
				break
			}
			if digest[j] > target[j] {
				solved = false
				break
			}
		}
		if solved {
			return encoded, nil
		}
	}

	fallbackSeedB64 := base64.StdEncoding.EncodeToString([]byte(`"` + seed + `"`))
	fallback := "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + fallbackSeedB64
	return fallback, fmt.Errorf("proof of work not solved, fallback: %s", fallback)
}
