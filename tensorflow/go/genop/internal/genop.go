// Copyright 2016 The TensorFlow Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package internal generates Go source code with functions for TensorFlow operations.
//
// The basic outline of the generated API is as follows:
//
// - One function for each TensorFlow operation
// - The arguments to the function are the inputs and required attributes of the operation
// - The function returns the outputs
// - A function is also generated for each optional attribute of the operation.
//
// There is a possibility that there are name collisions between the functions
// generated for ops and the functions generated for optional attributes. For
// now, we ignore those, but will need to revisit if a collision is actually
// encountered.
package internal

// #include "tensorflow/c/c_api.h"
import "C"

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/template"
	"unsafe"

	"github.com/golang/protobuf/proto"
	pb "github.com/tensorflow/tensorflow/tensorflow/go/genop/internal/proto/tensorflow/core/framework"
)

// GenerateFunctionsForRegisteredOps writes a Go source code file to w
// containing functions for each TensorFlow operation registered in the address
// space of the calling process.
func GenerateFunctionsForRegisteredOps(w io.Writer) error {
	ops, err := registeredOps()
	if err != nil {
		return err
	}
	return generateFunctionsForOps(w, ops)
}

func registeredOps() (*pb.OpList, error) {
	buf := C.TF_GetAllOpList()
	defer C.TF_DeleteBuffer(buf)
	var (
		list = new(pb.OpList)
		size = int(buf.length)
		// A []byte backed by C memory.
		// See: https://github.com/golang/go/wiki/cgo#turning-c-arrays-into-go-slices
		data = (*[1 << 30]byte)(unsafe.Pointer(buf.data))[:size:size]
		err  = proto.Unmarshal(data, list)
	)
	return list, err
}

func generateFunctionsForOps(w io.Writer, ops *pb.OpList) error {
	thisPackage := reflect.TypeOf(tmplArgs{}).PkgPath()
	if err := tmplHeader.Execute(w, thisPackage); err != nil {
		return err
	}
	blacklist := map[string]bool{
		"Const":           true,
		"PyFunc":          true,
		"PyFuncStateless": true,
	}
	for _, op := range ops.Op {
		if blacklist[op.Name] {
			continue
		}
		if err := generateFunctionForOp(w, op); err != nil {
			return err
		}
	}
	return nil
}

func generateFunctionForOp(w io.Writer, op *pb.OpDef) error {
	if strings.HasPrefix(op.Name, "_") { // Internal operation
		return nil
	}
	// Ignore operations where the Go types corresponding to the TensorFlow
	// type haven't been worked out (such as "func"s).
	for _, a := range op.Attr {
		if _, err := goType(a.Type); err != nil {
			return nil
		}
	}
	// Also, haven't figured out reference types yet, so ignore those too.
	for _, a := range op.InputArg {
		if a.IsRef {
			return nil
		}
	}
	for _, a := range op.OutputArg {
		if a.IsRef {
			return nil
		}
	}
	if op.Summary == "" {
		// Undocumented operation, perhaps a sign of not being ready to
		// export.
		return nil
	}
	return tmplOp.Execute(w, newTmplArgs(op))
}

var (
	// Go keywords that cannot be used as identifiers.
	// From https://golang.org/ref/spec#Keywords
	keywords = []string{
		"break", "default", "func", "interface", "select", "case",
		"defer", "go", "map", "struct", "chan", "else", "goto",
		"package", "switch", "const", "fallthrough", "if", "range",
		"type", "continue", "for", "import", "return", "var",
	}

	tmplHeader = template.Must(template.New("header").Parse(`// DO NOT EDIT
// This file was machine generated by {{.}}
//
// WARNING: This generation of wrapper function for TensorFlow ops is in an
// experimental state. The generated API can change without notice.

package op

import tf "github.com/tensorflow/tensorflow/tensorflow/go"

// optionalAttr is an intentionally un-exported type to hide
// details of how optional attributes to operations are implemented.
type optionalAttr map[string]interface{}

func makeOutputList(op *tf.Operation, start int, output string) ([]tf.Output, int, error) {
	size, err := op.OutputListSize(output)
	if err != nil {
		return nil, start, err
	}
	list := make([]tf.Output, size)
	for i := 0; i < size; i++ {
		list[i] = op.Output(start + i)
	}
	return list, start + size, nil
}
`))

	tmplOp = template.Must(template.New("op").Funcs(template.FuncMap{
		"MakeComment": makeComment,
		"GoType":      goType,
		"CamelCase":   camelCase,
		"Identifier":  identifier,
		"IsListArg":   isListArg,
		"IsListAttr":  isListAttr,
	}).Parse(`
{{if .OptionalAttrs -}}
{{/* Type for specifying all optional attributes. */ -}}
// {{.Op.Name}}Attr is an optional argument to {{.Op.Name}}.
type {{.Op.Name}}Attr func(optionalAttr)

{{range .OptionalAttrs}}
// {{$.Op.Name}}{{CamelCase .Name}} sets the optional {{.Name}} attribute to value.
{{- if .Description}}
//
// value: {{MakeComment .Description}}
{{- end}}
// If not specified, defaults to {{.DefaultValue}}
{{- if .HasMinimum}}
//
// {{if IsListAttr .}}REQUIRES: len(value) >= {{.Minimum}}{{else}}REQUIRES: value >= {{.Minimum}}{{end}}
{{- end}}
func {{$.Op.Name}}{{CamelCase .Name}}(value {{GoType .Type}}) {{$.Op.Name}}Attr {
	return func(m optionalAttr) {
		m[{{printf "%q" .Name}}] = value
	}
}
{{end}}
{{end}}

{{- /* Create a godoc friendly comment. */ -}}

// {{MakeComment .Op.Summary}}

{{- with .Op.Deprecation}}
//
// DEPRECATED at GraphDef version {{.Version}}: {{.Explanation}}
{{- end -}}

{{- with .Op.Description}}
//
// {{MakeComment .}}
{{- end -}}

{{- if .DescribeArguments}}
//
// Arguments:
{{- range .Op.InputArg}}
//	{{if .Description}}{{Identifier .Name}}: {{MakeComment .Description}}{{end}}
{{- end -}}
{{- range .RequiredAttrs}}
//	{{if .Description}}{{Identifier .Name}}: {{MakeComment .Description}}{{end}}
{{- end -}}
{{- end -}}

{{- if (not .Op.OutputArg) }}
//
// Returns the created operation.
{{- else }}
{{- if .DescribeOutputs}}
//
{{- if ((len .Op.OutputArg) eq 1) }}
// Returns {{range .Op.OutputArg}}{{MakeComment .Description}}{{end}}
{{- else }}
// Returns:
{{- range .Op.OutputArg}}
//	{{Identifier .Name}}{{if .Description}}: {{MakeComment .Description}}{{end}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*

  The function signature.
  Since OpDef.Name is in CamelCase, it cannot conflict with a reserved keyword in Golang
*/}}
func {{.Op.Name}}

{{- /*
  Fill in input arguments:
  (1) The Scope
  (2) All input arguments (which may be either []tf.Output or tf.Output)
  (3) All required attributes
  (4) Variadic list of optional attributes
*/ -}}

(scope *Scope
{{- range $i, $a := .Op.InputArg}}, {{Identifier $a.Name}} {{if IsListArg $a}}[]{{end}}tf.Output{{end -}}
{{range $i, $a := .RequiredAttrs}}, {{Identifier $a.Name}} {{GoType $a.Type}}{{end -}}
{{if .OptionalAttrs}}, optional ...{{.Op.Name}}Attr{{end -}}
)

{{- /* Construct outputs: len(OpDef.OutputArg) or a *tf.Operation */ -}}

{{if .Op.OutputArg -}}
({{range $i,$a := .Op.OutputArg}}{{if $i}}, {{end}}{{Identifier $a.Name}} {{if IsListArg $a}}[]{{end}}tf.Output{{end -}})
{{- else -}}
(o *tf.Operation)
{{- end }} {
	if scope.Err() != nil {
		return
	}
	{{if .HasAttrs -}}
	attrs := map[string]interface{}{ {{- range .RequiredAttrs}}{{printf "%q" .Name}}: {{Identifier .Name}},{{end}}}
	{{if .OptionalAttrs -}}
	for _, a := range optional {
		a(attrs)
	}
	{{end -}}
	{{end -}}
	opspec := tf.OpSpec{
		Type: {{printf "%q" .Op.Name}},
		{{if .Op.InputArg -}}
		Input: []tf.Input{
			{{range .Op.InputArg}}{{if IsListArg .}}tf.OutputList({{Identifier .Name}}){{else}}{{Identifier .Name}}{{end}}, {{end}}
		},
		{{- end}}
		{{- if .HasAttrs}}
		Attrs: attrs,
		{{- end}}
	}
	{{- if .Op.OutputArg}}
	{{- if .HasListOutput}}
	op := scope.AddOperation(opspec)
	if scope.Err() != nil {
		return
	}
	var idx int
	var err error
	{{- range $i, $a := .Op.OutputArg}}
	{{- if IsListArg $a}}
	if {{Identifier .Name}}, idx, err = makeOutputList(op, idx, {{printf "%q" .Name}}); err != nil {
		scope.UpdateErr({{printf "%q" $.Op.Name}}, err)
		return
	}
	{{- else }}
	{{Identifier .Name}} = op.Output(idx)
	{{- end }}{{- /* if IsListArg */}}
	{{- end }}{{- /* range .Op.OutputArg */}}
	return {{range $i, $a := .Op.OutputArg}}{{if $i}}, {{end}}{{Identifier .Name}}{{end}}
	{{- else }}
	op := scope.AddOperation(opspec)
	return {{range $i, $a := .Op.OutputArg}}{{if $i}}, {{end}}op.Output({{$i}}){{end}}
	{{- end }}{{- /* if .HasListOutput */}}
	{{- else }}
	return scope.AddOperation(opspec)
	{{- end }}{{- /* if .Op.OutputArg */}}
}
`))
)

type tmplArgs struct {
	Op *pb.OpDef
	// Op.Attr is split into two categories
	// (1) Required: These must be specified by the client and are thus
	//     included in the function signature.
	// (2) Optional: These need not be specified (as they have default
	//     values) and thus do not appear in the function signature.
	RequiredAttrs []*pb.OpDef_AttrDef
	OptionalAttrs []*pb.OpDef_AttrDef
}

func newTmplArgs(op *pb.OpDef) *tmplArgs {
	ret := tmplArgs{Op: op}
	if len(op.Attr) == 0 {
		return &ret
	}
	// Attributes related to the InputArg's type are inferred automatically
	// and are not exposed to the client.
	inferred := make(map[string]bool)
	for _, in := range op.InputArg {
		switch {
		case in.TypeAttr != "":
			inferred[in.TypeAttr] = true
		case in.TypeListAttr != "":
			inferred[in.TypeListAttr] = true
		}
		if in.NumberAttr != "" {
			inferred[in.NumberAttr] = true
		}
	}
	for _, attr := range op.Attr {
		if inferred[attr.Name] {
			continue
		}
		if attr.DefaultValue == nil {
			ret.RequiredAttrs = append(ret.RequiredAttrs, attr)
		} else {
			ret.OptionalAttrs = append(ret.OptionalAttrs, attr)
		}
	}
	return &ret
}

func (a *tmplArgs) HasAttrs() bool { return len(a.RequiredAttrs)+len(a.OptionalAttrs) > 0 }
func (a *tmplArgs) DescribeArguments() bool {
	for _, arg := range a.Op.InputArg {
		if arg.Description != "" {
			return true
		}
	}
	for _, attr := range a.RequiredAttrs {
		if attr.Description != "" {
			return true
		}
	}
	return false

}
func (a *tmplArgs) DescribeOutputs() bool {
	for _, arg := range a.Op.OutputArg {
		if arg.Description != "" {
			return true
		}
	}
	return false
}
func (a *tmplArgs) HasListOutput() bool {
	for _, arg := range a.Op.OutputArg {
		if isListArg(arg) {
			return true
		}
	}
	return false
}

func makeComment(lines string) string {
	return strings.Join(strings.SplitAfter(lines, "\n"), "// ")
}

// goType converts a TensorFlow "type" ('string', 'int', 'list(string)' etc.)
// to the corresponding type in Go.
func goType(tfType string) (string, error) {
	list, tfType := parseTFType(tfType)
	var gotype string
	switch tfType {
	case "int":
		gotype = "int64"
	case "float":
		gotype = "float32"
	case "bool":
		gotype = "bool"
	case "type":
		gotype = "tf.DataType"
	case "shape":
		gotype = "tf.Shape"
	case "tensor":
		gotype = "tf.Tensor"
	case "string":
		gotype = "string"
	default:
		return "", fmt.Errorf("%q is not a recognized DataType", tfType)
	}
	if list {
		gotype = "[]" + gotype
	}
	return gotype, nil
}

func camelCase(snakeCase string) string {
	words := strings.Split(snakeCase, "_")
	for i, w := range words {
		words[i] = strings.ToUpper(string(w[0])) + w[1:]
	}
	return strings.Join(words, "")
}

// identifier creates an identifier for s usable in the generated Go source
// code.
//
// Avoids collisions with keywords and other identifiers used in the generated
// code.
func identifier(s string) string {
	// Identifiers used in the generated code.
	if s == "tf" || s == "scope" || s == "err" || s == "op" {
		return s + "_"
	}
	for _, k := range keywords {
		if s == k {
			// Alternatively, make the first letter upper case.
			return s + "_"
		}
	}
	return s
}

func isListArg(argdef *pb.OpDef_ArgDef) bool {
	return argdef.TypeListAttr != "" || argdef.NumberAttr != ""
}

func isListAttr(attrdef *pb.OpDef_AttrDef) bool {
	list, _ := parseTFType(attrdef.Type)
	return list
}

func parseTFType(tfType string) (list bool, typ string) {
	const (
		listPrefix = "list("
		listSuffix = ")"
	)
	if strings.HasPrefix(tfType, listPrefix) && strings.HasSuffix(tfType, listSuffix) {
		return true, strings.TrimSuffix(strings.TrimPrefix(tfType, listPrefix), listSuffix)
	}
	return false, tfType
}
