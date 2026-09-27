// Generate public DTO schemas from the actual Go wire types. Run from repo root:
// go run ./api/generate && python3 api/generate_protocol.py
package main

import (
	"encoding/json"
	"os"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/internal/platform"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

type schema = map[string]any

var defs = map[string]schema{}
var names = map[reflect.Type]string{}
var rawType = reflect.TypeOf(json.RawMessage{})
var timeType = reflect.TypeOf(time.Time{})

func makeSchema(t reflect.Type) schema {
	if t == timeType {
		return schema{"type": "string", "format": "date-time"}
	}
	if t == rawType {
		return schema{"description": "Ruleset-defined JSON extension value; consult the locked plugin version for this field's schema."}
	}
	if t.Kind() == reflect.Pointer {
		return schema{"anyOf": []schema{makeSchema(t.Elem()), {"type": "null"}}}
	}
	switch t.Kind() {
	case reflect.String:
		return schema{"type": "string"}
	case reflect.Bool:
		return schema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return schema{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return schema{"type": "number"}
	case reflect.Slice:
		return schema{"type": []string{"array", "null"}, "items": makeSchema(t.Elem())}
	case reflect.Array:
		return schema{"type": "array", "minItems": t.Len(), "maxItems": t.Len(), "items": makeSchema(t.Elem())}
	case reflect.Map:
		return schema{"type": "object", "additionalProperties": makeSchema(t.Elem())}
	case reflect.Interface:
		return schema{"description": "Explicit user-supplied metadata JSON; not an engine state or private observation."}
	case reflect.Struct:
		name := names[t]
		if name == "" {
			name = strings.ToUpper(path.Base(t.PkgPath())[:1]) + path.Base(t.PkgPath())[1:] + t.Name()
			names[t] = name
		}
		if _, ok := defs[name]; ok {
			return schema{"$ref": "#/$defs/" + name}
		}
		defs[name] = schema{}
		properties := map[string]any{}
		required := []string{}
		var fields func(reflect.Type)
		fields = func(st reflect.Type) {
			for i := 0; i < st.NumField(); i++ {
				f := st.Field(i)
				if f.PkgPath != "" {
					continue
				}
				tag := f.Tag.Get("json")
				if tag == "-" {
					continue
				}
				if f.Anonymous && tag == "" {
					fields(f.Type)
					continue
				}
				parts := strings.Split(tag, ",")
				key := parts[0]
				if key == "" {
					key = f.Name
				}
				properties[key] = makeSchema(f.Type)
				if !strings.Contains(tag, ",omitempty") {
					required = append(required, key)
				}
			}
		}
		fields(t)
		defs[name] = schema{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
		return schema{"$ref": "#/$defs/" + name}
	default:
		panic("unsupported public DTO type: " + t.String())
	}
}

func main() {
	values := map[string]any{"User": auth.User{}, "Room": platform.Room{}, "Seat": platform.Seat{}, "Action": platform.Action{}, "Manifest": rulesdk.Manifest{}, "Option": rulesdk.Option{}, "Score": rulesdk.Score{}, "MCRParticipantView": mcr.ParticipantView{}, "MCRSpectatorView": mcr.SpectatorView{}, "StatisticResponse": platform.StatisticResponse{}, "StatisticDimensions": platform.StatisticDimensions{}, "StatisticMetric": platform.StatisticMetric{}, "StatisticGroup": platform.StatisticGroup{}, "StatisticMatch": platform.StatisticMatch{}, "StatisticProfile": platform.StatisticProfile{}, "BotRuntimeStatus": platform.BotRuntimeStatus{}, "BotRecentError": platform.BotRecentError{}}
	for name, value := range values {
		names[reflect.TypeOf(value)] = name
	}
	for _, value := range values {
		makeSchema(reflect.TypeOf(value))
	}
	action := defs["Action"]["properties"].(map[string]any)
	action["type"] = schema{"const": "submit_action"}
	action["protocol_version"] = schema{"const": "1.0", "default": "1.0"}
	action["command_id"] = schema{"type": "string", "minLength": 8, "maxLength": 128}
	defs["Action"]["description"] = "Retry the entire identical command with the same command_id. Recorded ACK is not adjudication. Human cookie or Bot short-session authentication remains mandatory."
	spectator := defs["MCRSpectatorView"]["properties"].(map[string]any)
	spectator["view_policy"] = schema{"const": "spectator_discard_only@1"}
	defs["MCRSpectatorView"]["description"] = "Strict discard-only projection at every phase, including final settlement and replay. The only card faces are discards[].kind. Never attach raw rule state, hidden hands, flower faces, meld faces, scoring explanations or tile entity IDs."
	b, err := json.MarshalIndent(schema{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "https://openmajiang.invalid/schemas/dto.json", "$defs": defs}, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile("api/schemas/dto.json", append(b, '\n'), 0644); err != nil {
		panic(err)
	}
}
