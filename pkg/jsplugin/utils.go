package jsplugin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Calcium-Ion/moejs"
	"github.com/QuantumNous/new-api/pkg/volcsign"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type volcSignRequest = volcsign.Request

// injectGlobals installs utils and console. currentCall returns the hook call
// running on runtime, or nil outside one; utils.fetch reads its fetcher and
// budget there, so no request data lives in the runtime's globals.
func injectGlobals(runtime *moejs.Runtime, identity func() string, now func() time.Time, logOutput func(string), currentCall func() *runtimeCall) error {
	utils := map[string]any{
		"fetch": runtime.Function("fetch", 1, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			call := currentCall()
			if call == nil {
				return moejs.Undefined(), fmt.Errorf("utils.fetch is not available outside hooks")
			}
			return call.fetch(r, moejs.Arg(args, 0))
		}),
		"hasCapability": runtime.Function("hasCapability", 1, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			name, err := stringValue(r, moejs.Arg(args, 0))
			if err != nil {
				return moejs.Undefined(), err
			}
			return moejs.Bool(HasCapability(name)), nil
		}),
		"json": map[string]any{
			"clone": runtime.Function("clone", 1, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
				if moejs.Arg(args, 0).IsUndefined() {
					return moejs.Undefined(), r.TypeError("json.clone requires a JSON value")
				}
				budget := &jsonStateBudget{ctx: context.Background(), bytes: MaxJSONToolBytes, nodes: maxJSONToolNodes, realm: r}
				value, err := newJSONStateNode(moejs.Arg(args, 0), 0, budget)
				if err != nil {
					var exception *moejs.Exception
					var interrupted *moejs.InterruptedError
					if errors.As(err, &exception) || errors.As(err, &interrupted) {
						return moejs.Undefined(), err
					}
					return moejs.Undefined(), r.TypeError("json.clone: %s", err)
				}
				return value.jsValue(r)
			}),
		},
		"unixNow": runtime.Function("unixNow", 0, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			return moejs.Int(now().Unix()), nil
		}),
		"jwtSignHS256": runtime.Function("jwtSignHS256", 2, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			var claims map[string]any
			if argument := moejs.Arg(args, 0); !argument.IsNullish() {
				exported, err := r.ToGoStrict(argument)
				if err != nil {
					return moejs.Undefined(), err
				}
				var ok bool
				if claims, ok = exported.(map[string]any); !ok {
					return moejs.Undefined(), r.TypeError("jwtSignHS256 claims must be an object")
				}
			}
			secret, err := stringValue(r, moejs.Arg(args, 1))
			if err != nil {
				return moejs.Undefined(), err
			}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(claims)).SignedString([]byte(secret))
			if err != nil {
				return moejs.Undefined(), err
			}
			return moejs.String(token), nil
		}),
		"hmacSHA256": runtime.Function("hmacSHA256", 2, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			message, err := stringValue(r, moejs.Arg(args, 0))
			if err != nil {
				return moejs.Undefined(), err
			}
			secret, err := stringValue(r, moejs.Arg(args, 1))
			if err != nil {
				return moejs.Undefined(), err
			}
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write([]byte(message))
			return moejs.String(hex.EncodeToString(mac.Sum(nil))), nil
		}),
		"base64": runtime.Function("base64", 1, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			value, err := stringValue(r, moejs.Arg(args, 0))
			if err != nil {
				return moejs.Undefined(), err
			}
			return moejs.String(base64.StdEncoding.EncodeToString([]byte(value))), nil
		}),
		"base64URL": runtime.Function("base64URL", 1, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			value, err := stringValue(r, moejs.Arg(args, 0))
			if err != nil {
				return moejs.Undefined(), err
			}
			return moejs.String(base64.RawURLEncoding.EncodeToString([]byte(value))), nil
		}),
		"base64URLDecode": runtime.Function("base64URLDecode", 1, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			value, err := stringValue(r, moejs.Arg(args, 0))
			if err != nil {
				return moejs.Undefined(), err
			}
			decoded, err := base64.RawURLEncoding.DecodeString(value)
			if err != nil {
				return moejs.Undefined(), err
			}
			return moejs.String(string(decoded)), nil
		}),
		"uuid": runtime.Function("uuid", 0, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			return moejs.String(uuid.NewString()), nil
		}),
		"volcSignV4": runtime.Function("volcSignV4", 1, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			// Fields are read by their Go names, as plugins have always
			// passed them.
			var request volcSignRequest
			argument := moejs.Arg(args, 0)
			if !argument.IsNullish() && !argument.IsObject() {
				return moejs.Undefined(), r.TypeError("volcSignV4 request must be an object")
			}
			if argument.IsObject() {
				object := argument.AsObject()
				fields := []struct {
					name   string
					target *string
				}{
					{"Method", &request.Method}, {"URL", &request.URL}, {"Body", &request.Body},
					{"AccessKey", &request.AccessKey}, {"SecretKey", &request.SecretKey},
					{"Region", &request.Region}, {"Service", &request.Service},
				}
				for _, field := range fields {
					value, err := object.GetProp(r, r.KeyFromGoString(field.name))
					if err != nil {
						return moejs.Undefined(), err
					}
					if *field.target, err = stringValue(r, value); err != nil {
						return moejs.Undefined(), err
					}
				}
				headers, err := object.GetProp(r, r.KeyFromGoString("Headers"))
				if err != nil {
					return moejs.Undefined(), err
				}
				if headers.IsObject() {
					keys, err := r.EnumerableOwnKeys(headers.AsObject())
					if err != nil {
						return moejs.Undefined(), err
					}
					request.Headers = make(map[string]string, len(keys))
					for _, key := range keys {
						value, err := headers.AsObject().GetProp(r, key)
						if err != nil {
							return moejs.Undefined(), err
						}
						if request.Headers[key.GoString()], err = stringValue(r, value); err != nil {
							return moejs.Undefined(), err
						}
					}
				}
				timestamp, err := object.GetProp(r, r.KeyFromGoString("Timestamp"))
				if err != nil {
					return moejs.Undefined(), err
				}
				if !timestamp.IsNullish() {
					seconds, err := r.ToNumber(timestamp)
					if err != nil {
						return moejs.Undefined(), err
					}
					if !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
						request.Timestamp = int64(seconds)
					}
				}
			}
			signed, err := signVolcV4(request, now)
			if err != nil {
				return moejs.Undefined(), err
			}
			return r.FromGo(signed)
		}),
	}
	if err := runtime.SetGlobal("utils", utils); err != nil {
		return err
	}
	return runtime.SetGlobal("console", map[string]any{
		"log": runtime.Function("log", 0, func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			parts := make([]string, len(args))
			for i, argument := range args {
				if argument.IsSymbol() {
					parts[i] = argument.String()
					continue
				}
				text, err := r.ToString(argument)
				if err != nil {
					return moejs.Undefined(), err
				}
				parts[i] = text.GoString()
			}
			if logOutput != nil {
				logOutput(identity() + " " + strings.Join(parts, " "))
			}
			return moejs.Undefined(), nil
		}),
	})
}

// stringValue reads a host function's string parameter: undefined and null
// read as "", other values as JavaScript's String(value).
func stringValue(r *moejs.Realm, value moejs.Value) (string, error) {
	if value.IsNullish() {
		return "", nil
	}
	if value.IsString() {
		return value.AsString().GoString(), nil
	}
	text, err := r.ToString(value)
	if err != nil {
		return "", err
	}
	return text.GoString(), nil
}

func signVolcV4(request volcSignRequest, now func() time.Time) (map[string]string, error) {
	return volcsign.Sign(request, now())
}
