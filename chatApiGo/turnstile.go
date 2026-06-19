package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

type OrderedMap struct {
	keys   []string
	values map[string]interface{}
}

func NewOrderedMap() *OrderedMap {
	return &OrderedMap{values: make(map[string]interface{})}
}

func (om *OrderedMap) Add(key string, val interface{}) {
	if _, exists := om.values[key]; !exists {
		om.keys = append(om.keys, key)
	}
	om.values[key] = val
}

type TurnstileFunc func(args ...interface{})

type TurnstileVM struct {
	processMap map[float64]interface{}
	result     string
	startTime  time.Time
}

func NewTurnstileVM() *TurnstileVM {
	return &TurnstileVM{
		processMap: make(map[float64]interface{}),
		startTime:  time.Now(),
	}
}

func turnstileToStr(val interface{}) string {
	if val == nil {
		return "undefined"
	}
	switch v := val.(type) {
	case string:
		special := map[string]string{
			"window.Math":            "[object Math]",
			"window.Reflect":         "[object Reflect]",
			"window.performance":     "[object Performance]",
			"window.localStorage":    "[object Storage]",
			"window.Object":          "function Object() { [native code] }",
			"window.Reflect.set":     "function set() { [native code] }",
			"window.performance.now": "function () { [native code] }",
			"window.Object.create":   "function create() { [native code] }",
			"window.Object.keys":     "function keys() { [native code] }",
			"window.Math.random":     "function random() { [native code] }",
		}
		if mapped, ok := special[v]; ok {
			return mapped
		}
		return v
	case float64:
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10) + ".0"
		}
		return fmt.Sprintf("%v", v)
	case bool:
		return strconv.FormatBool(v)
	case []interface{}:
		allStrings := true
		strs := make([]string, len(v))
		for i, item := range v {
			if s, ok := item.(string); ok {
				strs[i] = s
			} else {
				allStrings = false
				break
			}
		}
		if allStrings {
			return strings.Join(strs, ",")
		}
		return fmt.Sprintf("%v", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func xorString(text, key string) string {
	if key == "" {
		return text
	}
	result := make([]byte, len(text))
	for i := 0; i < len(text); i++ {
		result[i] = text[i] ^ key[i%len(key)]
	}
	return string(result)
}

func (vm *TurnstileVM) Solve(dx, p string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(dx)
	if err != nil {
		return "", fmt.Errorf("base64 decode dx: %w", err)
	}

	decrypted := xorString(string(decoded), p)

	var tokenList []interface{}
	if err := json.Unmarshal([]byte(decrypted), &tokenList); err != nil {
		return "", fmt.Errorf("parse token list: %w", err)
	}

	vm.initFunctions(p, tokenList)

	for _, token := range tokenList {
		tokArr, ok := token.([]interface{})
		if !ok || len(tokArr) == 0 {
			continue
		}
		fnID, ok := tokArr[0].(float64)
		if !ok {
			continue
		}
		fnVal, exists := vm.processMap[fnID]
		if !exists {
			continue
		}
		fn, ok := fnVal.(TurnstileFunc)
		if !ok {
			continue
		}

		func() {
			defer func() {
				recover()
			}()
			fn(tokArr[1:]...)
		}()
	}

	return vm.result, nil
}

func (vm *TurnstileVM) initFunctions(p string, tokenList []interface{}) {
	pm := vm.processMap

	pm[1] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])
		valE := turnstileToStr(pm[e])
		valT := turnstileToStr(pm[t])
		pm[e] = xorString(valE, valT)
	})

	pm[2] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		pm[e] = args[1]
	})

	pm[3] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 1 {
			return
		}
		e := turnstileToStr(args[0])
		vm.result = base64.StdEncoding.EncodeToString([]byte(e))
	})

	pm[5] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])

		current := pm[e]
		incoming := pm[t]

		switch curVal := current.(type) {
		case []interface{}:
			pm[e] = append(curVal, incoming)
		case string:
			pm[e] = curVal + turnstileToStr(incoming)
		case float64:
			pm[e] = turnstileToStr(curVal) + turnstileToStr(incoming)
		default:
			pm[e] = "NaN"
		}
	})

	pm[6] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 3 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])
		n := toFloat64(args[2])

		tv, tOK := pm[t].(string)
		nv, nOK := pm[n].(string)
		if tOK && nOK {
			val := tv + "." + nv
			if val == "window.document.location" {
				pm[e] = "https://chatgpt.com/"
			} else {
				pm[e] = val
			}
		}
	})

	pm[7] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		target := turnstileToStr(pm[e])

		if target == "window.Reflect.set" {
			var fnArgs []interface{}
			for _, arg := range args[1:] {
				fnArgs = append(fnArgs, pm[toFloat64(arg)])
			}
			if len(fnArgs) < 3 {
				return
			}
			obj, _ := fnArgs[0].(*OrderedMap)
			keyName := turnstileToStr(fnArgs[1])
			if obj != nil {
				obj.Add(keyName, fnArgs[2])
			}
		}
	})

	pm[8] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])
		pm[e] = pm[t]
	})

	pm[14] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])
		strVal := turnstileToStr(pm[t])
		var parsed interface{}
		if err := json.Unmarshal([]byte(strVal), &parsed); err == nil {
			pm[e] = parsed
		}
	})

	pm[15] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])
		data, _ := json.Marshal(pm[t])
		pm[e] = string(data)
	})

	pm[17] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])

		target := turnstileToStr(pm[t])

		var callArgs []interface{}
		for _, arg := range args[2:] {
			callArgs = append(callArgs, pm[toFloat64(arg)])
		}

		switch target {
		case "window.performance.now":
			elapsedNs := time.Now().UnixNano() - vm.startTime.UnixNano()
			ms := float64(elapsedNs+int64(rand.Float64()*1000)) / 1e6
			pm[e] = ms
		case "window.Object.create":
			pm[e] = NewOrderedMap()
		case "window.Object.keys":
			if len(callArgs) > 0 {
				first := turnstileToStr(callArgs[0])
				if first == "window.localStorage" {
					pm[e] = []interface{}{
						"STATSIG_LOCAL_STORAGE_INTERNAL_STORE_V4",
						"STATSIG_LOCAL_STORAGE_STABLE_ID",
						"client-correlated-secret",
						"oai/apps/capExpiresAt",
						"oai-did",
						"STATSIG_LOCAL_STORAGE_LOGGING_REQUEST",
						"UiState.isNavigationCollapsed.1",
					}
				}
			}
		case "window.Math.random":
			pm[e] = rand.Float64()
		default:
			if fn, ok := pm[t].(TurnstileFunc); ok {
				fn(args[2:]...)
			}
		}
	})

	pm[18] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 1 {
			return
		}
		e := toFloat64(args[0])
		strVal := turnstileToStr(pm[e])
		decoded, err := base64.StdEncoding.DecodeString(strVal)
		if err == nil {
			pm[e] = string(decoded)
		}
	})

	pm[19] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 1 {
			return
		}
		e := toFloat64(args[0])
		strVal := turnstileToStr(pm[e])
		pm[e] = base64.StdEncoding.EncodeToString([]byte(strVal))
	})

	pm[20] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 3 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])
		n := toFloat64(args[2])

		valE := turnstileToStr(pm[e])
		valT := turnstileToStr(pm[t])

		if valE == valT {
			if fn, ok := pm[n].(TurnstileFunc); ok {
				var fnArgs []interface{}
				for _, arg := range args[3:] {
					fnArgs = append(fnArgs, pm[toFloat64(arg)])
				}
				fn(fnArgs...)
			}
		}
	})

	pm[21] = TurnstileFunc(func(args ...interface{}) {})

	pm[23] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 2 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])

		if pm[e] != nil {
			if fn, ok := pm[t].(TurnstileFunc); ok {
				var fnArgs []interface{}
				for _, arg := range args[2:] {
					fnArgs = append(fnArgs, pm[toFloat64(arg)])
				}
				fn(fnArgs...)
			}
		}
	})

	pm[24] = TurnstileFunc(func(args ...interface{}) {
		if len(args) < 3 {
			return
		}
		e := toFloat64(args[0])
		t := toFloat64(args[1])
		n := toFloat64(args[2])

		tv, tOK := pm[t].(string)
		nv, nOK := pm[n].(string)
		if tOK && nOK {
			pm[e] = tv + "." + nv
		}
	})

	pm[9] = tokenList
	pm[10] = "window"
	pm[16] = p
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	default:
		return 0
	}
}
