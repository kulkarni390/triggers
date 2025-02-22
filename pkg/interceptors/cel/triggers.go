/*
Copyright 2020 The Tekton Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

		http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cel

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter/functions"
	"github.com/tektoncd/triggers/pkg/interceptors"
	"sigs.k8s.io/yaml"

	triggersv1 "github.com/tektoncd/triggers/pkg/apis/triggers/v1beta1"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// Triggers returns a cel.EnvOption to configure extended functions for
// Tekton CEL interceptor expressions.
//
// match
//
// Returns true if the specified provided header matches the provided string
// key.
//
// It is case insensitive; the header is canonicalised using the rules described
// here https://golang.org/pkg/net/textproto/#CanonicalMIMEHeaderKey
//
//     <header>.match(<string>, <string>) -> <bool>
//
// Examples:
//
//     header.match('X-Github-Event', 'push')
//
// canonical
//
// Can only be applied to the `header` key in the CEL expression context.
//
// Gets the first value associated with the given key. If there are no values
// associated with the key, returns "".
//
// It is case insensitive; the header is canonicalised using the rules described
// here https://golang.org/pkg/net/textproto/#CanonicalMIMEHeaderKey
//
//     <header>.canonical(<string>) -> <string>
//
// Examples:
//
//     header.canonical('X-Github-Event') // returns 'push'
//
// decodeb64
//
// Returns the base64 decoded representation of a string value.
//
// Returns an error if the value is not valid base64 data.
//
//     <string>.decodeb64() -> <string>
//
// Examples:
//
//     body.value.decodeb64() // returns decoded version
//
// truncate
//
// Returns a truncated copy of the string, at the given position.
//
// If the requested length is longer than the actual length, then the string
// will be returned unchanged.
//
//     <string>.truncate(<int>) -> <string>
//
// Examples:
//
//     body.request.sha.truncate(7) // returns truncated string
//
// compareSecret
//
// Returns true if the string matches the value from a Kubernetes secret with
// the provided key, secret-name, namespace combination.
//
//     <string>.compareSecret(<string>, <string>, <string>) -> <bool>
//
// Examples:
//
//     header.canonical('X-Secret-Token').compareSecret('key', 'secret-name', 'namespace')
//
// There is also an alternative compareSecret which accepts two parameters
//
// Returns true if the string matches the value from a Kubernetes secret with
// the provided key, secret-name combination, this default to the namespace the
// event-listener is in.
//
//     <string>.compareSecret(<string>, <string>) -> <bool>
//
// Examples:
//
//     header.canonical('X-Secret-Token').compareSecret('key', 'secret-name')
//
// parseJSON
//
// Parses a string into a map of strings to dynamic values.
//
//     <string>.parseJSON() -> map<string, dyn>
//
// Examples:
//
//     body.field.parseJSON().item
//
// parseURL
//
// Parses a URL (in the form of a string) into a map with keys representing the
// elements of the URL.
//
//     <string>.parseURL() -> map<string, dyn>
//
// Examples:
//
//     'https://example.com/testing'.parseURL().host == 'example.com'
//
// parseYAML
//
// Parses a YAML string into a map of strings to dynamic values
//
// 		<string>.parseYAML() -> map<string, dyn>
//
// Examples:
//
// 		body.field.parseYAML().item
//
// marshalJSON
//
// Returns the JSON encoding of 'jsonObjectOrList'.
//
// 		<jsonObjectOrList>.marshalJSON() -> <string>
//
// Examples:
//
// 		body.jsonObjectOrList.marshalJSON()

// Triggers creates and returns a new cel.Lib with the triggers extensions.
func Triggers(ctx context.Context, ns string, sg interceptors.SecretGetter) cel.EnvOption {
	return cel.Lib(triggersLib{ctx: ctx, defaultNS: ns, secretGetter: sg})
}

type triggersLib struct {
	ctx          context.Context
	defaultNS    string
	secretGetter interceptors.SecretGetter
}

func (triggersLib) CompileOptions() []cel.EnvOption {
	mapStrDyn := decls.NewMapType(decls.String, decls.Dyn)
	return []cel.EnvOption{
		cel.Declarations(
			decls.NewFunction("match",
				decls.NewInstanceOverload("match_map_string_string",
					[]*exprpb.Type{mapStrDyn, decls.String, decls.String}, decls.Bool)),
			decls.NewFunction("canonical",
				decls.NewInstanceOverload("canonical_map_string",
					[]*exprpb.Type{mapStrDyn, decls.String}, decls.String)),
			decls.NewFunction("decodeb64",
				decls.NewInstanceOverload("decodeb64_string",
					[]*exprpb.Type{decls.String}, decls.String)),
			decls.NewFunction("truncate",
				decls.NewInstanceOverload("truncate_string_uint",
					[]*exprpb.Type{decls.String, decls.Int}, decls.String)),
			decls.NewFunction("compareSecret",
				decls.NewInstanceOverload("compareSecret_string_string_string",
					[]*exprpb.Type{decls.String, decls.String, decls.String, decls.String}, decls.Bool)),
			decls.NewFunction("parseJSON",
				decls.NewInstanceOverload("parseJSON_string",
					[]*exprpb.Type{decls.String}, mapStrDyn)),
			decls.NewFunction("parseYAML",
				decls.NewInstanceOverload("parseYAML_string",
					[]*exprpb.Type{decls.String}, mapStrDyn)),
			decls.NewFunction("parseURL",
				decls.NewInstanceOverload("parseURL_string",
					[]*exprpb.Type{decls.String}, mapStrDyn)),
			decls.NewFunction("compareSecret",
				decls.NewInstanceOverload("compareSecret_string_string",
					[]*exprpb.Type{decls.String, decls.String, decls.String}, decls.Bool)),
			decls.NewFunction("marshalJSON",
				decls.NewInstanceOverload("marshalJSON_map",
					[]*exprpb.Type{mapStrDyn}, decls.String)))}
}

func (t triggersLib) ProgramOptions() []cel.ProgramOption {
	return []cel.ProgramOption{
		cel.Functions(
			&functions.Overload{
				Operator: "match",
				Function: matchHeader},
			&functions.Overload{
				Operator: "canonical",
				Binary:   canonicalHeader},
			&functions.Overload{
				Operator: "truncate",
				Binary:   truncateString},
			&functions.Overload{
				Operator: "decodeb64",
				Unary:    decodeB64String},
			&functions.Overload{
				Operator: "parseJSON",
				Unary:    parseJSONString},
			&functions.Overload{
				Operator: "parseYAML",
				Unary:    parseYAMLString},
			&functions.Overload{
				Operator: "parseURL",
				Unary:    parseURLString},
			&functions.Overload{
				Operator: "compareSecret",
				Function: makeCompareSecret(t.ctx, t.defaultNS, t.secretGetter)},
			&functions.Overload{
				Operator: "marshalJSON",
				Unary:    marshalJSON},
		)}
}

func matchHeader(vals ...ref.Val) ref.Val {
	h, err := vals[0].ConvertToNative(reflect.TypeOf(http.Header{}))
	if err != nil {
		return types.NewErr("failed to convert to http.Header: %w", err)
	}

	key, ok := vals[1].(types.String)
	if !ok {
		return types.ValOrErr(key, "unexpected type '%v' passed to match", vals[1].Type())
	}

	val, ok := vals[2].(types.String)
	if !ok {
		return types.ValOrErr(val, "unexpected type '%v' passed to match", vals[2].Type())
	}

	return types.Bool(h.(http.Header).Get(string(key)) == string(val))
}

func truncateString(lhs, rhs ref.Val) ref.Val {
	str, ok := lhs.(types.String)
	if !ok {
		return types.ValOrErr(str, "unexpected type '%v' passed to truncate", lhs.Type())
	}

	n, ok := rhs.(types.Int)
	if !ok {
		return types.ValOrErr(n, "unexpected type '%v' passed to truncate", rhs.Type())
	}

	return str[:max(n, types.Int(len(str)))]
}

func canonicalHeader(lhs, rhs ref.Val) ref.Val {
	h, err := lhs.ConvertToNative(reflect.TypeOf(http.Header{}))
	if err != nil {
		return types.NewErr("failed to convert to http.Header: %w", err)
	}

	key, ok := rhs.(types.String)
	if !ok {
		return types.ValOrErr(key, "unexpected type '%v' passed to canonical", rhs.Type())
	}

	return types.String(h.(http.Header).Get(string(key)))
}

func decodeB64String(val ref.Val) ref.Val {
	str, ok := val.(types.String)
	if !ok {
		return types.ValOrErr(str, "unexpected type '%v' passed to decodeB64", val.Type())
	}
	dec, err := base64.StdEncoding.DecodeString(str.Value().(string))
	if err != nil {
		return types.NewErr("failed to decode '%v' in decodeB64: %w", str, err)
	}
	return types.String(dec)
}

// makeCompareSecret creates and returns a functions.FunctionOp that wraps the
// ns and client in a closure with a function that can compare the string.
func makeCompareSecret(ctx context.Context, defaultNS string, sg interceptors.SecretGetter) functions.FunctionOp {
	return func(vals ...ref.Val) ref.Val {
		var ok bool
		compareString, ok := vals[0].(types.String)
		if !ok {
			return types.ValOrErr(compareString, "unexpected type '%v' passed to compareSecret", vals[0].Type())
		}

		secretNS := types.String(defaultNS)

		secretName, ok := vals[2].(types.String)
		if !ok {
			return types.ValOrErr(secretName, "unexpected type '%v' passed to compareSecret", vals[2].Type())
		}

		secretKey, ok := vals[1].(types.String)
		if !ok {
			return types.ValOrErr(secretKey, "unexpected type '%v' passed to compareSecret", vals[3].Type())
		}

		secretRef := &triggersv1.SecretRef{
			SecretKey:  string(secretKey),
			SecretName: string(secretName),
		}
		// GetSecretToken uses request as a cache key to cache secret lookup. Since multiple
		// triggers execute concurrently in separate goroutines, this cache is not very effective
		// for this use case
		secretToken, err := sg.Get(ctx, string(secretNS), secretRef)
		if err != nil {
			return types.NewErr("failed to find secret '%#v' in compareSecret: %w", *secretRef, err)
		}
		return types.Bool(subtle.ConstantTimeCompare(secretToken, []byte(compareString)) == 1)
	}
}

func parseJSONString(val ref.Val) ref.Val {
	str, ok := val.(types.String)
	if !ok {
		return types.ValOrErr(str, "unexpected type '%v' passed to parseJSON", val.Type())
	}
	decodedVal := map[string]interface{}{}
	err := json.Unmarshal([]byte(str), &decodedVal)
	if err != nil {
		return types.NewErr("failed to decode '%v' in parseJSON: %w", str, err)
	}
	r, err := types.NewRegistry()
	if err != nil {
		return types.NewErr("failed to create a new registry in parseJSON: %w", err)
	}
	return types.NewDynamicMap(r, decodedVal)
}

func parseYAMLString(val ref.Val) ref.Val {
	str, ok := val.(types.String)
	if !ok {
		return types.ValOrErr(str, "unexpected type '%v' passed to parseYAML", val.Type())
	}
	decodedVal := map[string]interface{}{}
	err := yaml.Unmarshal([]byte(str), &decodedVal)
	if err != nil {
		return types.NewErr("failed to decode '%v' in parseYAML: %w", str, err)
	}
	r, err := types.NewRegistry()
	if err != nil {
		return types.NewErr("failed to create a new registry in parseJSON: %w", err)
	}
	return types.NewDynamicMap(r, decodedVal)
}

func parseURLString(val ref.Val) ref.Val {
	str, ok := val.(types.String)
	if !ok {
		return types.ValOrErr(str, "unexpected type '%v' passed to parseURL", val.Type())
	}

	parsed, err := url.Parse(string(str))
	if err != nil {
		return types.NewErr("failed to decode '%v' in parseURL: %w", str, err)
	}
	r, err := types.NewRegistry()
	if err != nil {
		return types.NewErr("failed to create a new registry in parseJSON: %w", err)
	}
	return types.NewDynamicMap(r, urlToMap(parsed))
}

func marshalJSON(val ref.Val) ref.Val {
	var typeDesc reflect.Type

	switch val.Type() {
	case types.MapType:
		typeDesc = mapType
	case types.ListType:
		typeDesc = listType
	default:
		return types.ValOrErr(val, "unexpected type '%v' passed to marshalJSON", val.Type())
	}

	nativeVal, err := val.ConvertToNative(typeDesc)
	if err != nil {
		return types.NewErr("failed to convert to native: %w", err)
	}

	marshaledVal, err := json.Marshal(nativeVal)
	if err != nil {
		return types.NewErr("failed to marshal to json: %w", err)
	}

	return types.String(marshaledVal)
}

func max(x, y types.Int) types.Int {
	switch x.Compare(y) {
	case types.IntNegOne:
		return x
	case types.IntOne:
		return y
	default:
		return x
	}
}

func urlToMap(u *url.URL) map[string]interface{} {
	// This doesn't return the RawPath.
	m := map[string]interface{}{
		"scheme":       u.Scheme,
		"host":         u.Host,
		"path":         u.Path,
		"rawQuery":     u.RawQuery,
		"fragment":     u.Fragment,
		"queryStrings": u.Query(),
		"query":        flatten(u.Query()),
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		m["auth"] = map[string]string{
			"username": u.User.Username(),
			"password": pass,
		}
	}
	return m
}

func flatten(uv url.Values) map[string]string {
	r := map[string]string{}
	for k, v := range uv {
		r[k] = strings.Join(v, ",")
	}
	return r
}
