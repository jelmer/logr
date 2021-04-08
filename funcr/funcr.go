/*
Copyright 2021 The logr Authors.

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

// Package funcr implements github.com/go-logr/logr.Logger in terms of
// an arbitrary "write" function.
package funcr

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-logr/logr"
)

// New returns a logr.Logger which is implemented by a function.
func New(fn func(prefix, args string), opts Options) logr.Logger {
	return fnlogger{
		level:     0,
		prefix:    "",
		values:    nil,
		write:     fn,
		logCaller: opts.LogCaller,
	}
}

type Options struct {
	// LogCaller tells funcr to add a "caller" key to some or all log lines.
	// This has some overhead, so some users might not want it.
	LogCaller MessageClass
}

type MessageClass int

const (
	None MessageClass = iota
	All
	Info
	Error
)

type fnlogger struct {
	level     int
	prefix    string
	values    []interface{}
	write     func(prefix, args string)
	logCaller MessageClass
}

// Magic string for intermediate frames that we should ignore.
const autogeneratedFrameName = "<autogenerated>"

// Cached depth of this interface's log functions.
var framesAtomic int32 // atomic

// Discover how many frames we need to climb to find the caller. This approach
// was suggested by Ian Lance Taylor of the Go team, so it *should* be safe
// enough (famous last words) and should survive changes in Go's optimizer.
//
// This assumes that all logging paths are the same depth from the caller,
// which should be a reasonable assumption since they are part of the same
// interface.
func framesToCaller() int {
	// Figuring out the current depth is somewhat expensive. Saving the value
	// amortizes most of that runtime cost.
	if atomic.LoadInt32(&framesAtomic) != 0 {
		return int(framesAtomic)
	}
	// 1 is the immediate caller.  3 should be too many.
	for i := 1; i < 3; i++ {
		_, file, _, _ := runtime.Caller(i + 1) // +1 for this function's frame
		if file != autogeneratedFrameName {
			atomic.StoreInt32(&framesAtomic, int32(i))
			return i
		}
	}
	return 1 // something went wrong, this is safe
}

func flatten(kvList ...interface{}) string {
	if len(kvList)%2 != 0 {
		kvList = append(kvList, "<no-value>")
	}
	// Empirically bytes.Buffer is faster than strings.Builder for this.
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	for i := 0; i < len(kvList); i += 2 {
		k, ok := kvList[i].(string)
		if !ok {
			k = fmt.Sprintf("<non-string-key-%d>", i/2)
		}
		v := kvList[i+1]

		if i > 0 {
			buf.WriteRune(' ')
		}
		buf.WriteRune('"')
		buf.WriteString(k)
		buf.WriteRune('"')
		buf.WriteRune('=')
		buf.WriteString(pretty(v))
	}
	return buf.String()
}

func pretty(value interface{}) string {
	return prettyWithFlags(value, 0)
}

const (
	flagRawString = 0x1
)

// TODO: This is not fast. Most of the overhead goes here.
func prettyWithFlags(value interface{}, flags uint32) string {
	// Handling the most common types without reflect is a small perf win.
	switch v := value.(type) {
	case bool:
		return strconv.FormatBool(v)
	case string:
		if flags&flagRawString > 0 {
			return v
		}
		// This is empirically faster than strings.Builder.
		return `"` + v + `"`
	case int:
		return strconv.FormatInt(int64(v), 10)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(int64(v), 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case uintptr:
		return strconv.FormatUint(uint64(v), 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}

	buf := bytes.NewBuffer(make([]byte, 0, 256))
	t := reflect.TypeOf(value)
	if t == nil {
		return "null"
	}
	v := reflect.ValueOf(value)
	switch t.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.String:
		if flags&flagRawString > 0 {
			return v.String()
		}
		// This is empirically faster than strings.Builder.
		return `"` + v.String() + `"`
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(int64(v.Int()), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(uint64(v.Uint()), 10)
	case reflect.Float32:
		return strconv.FormatFloat(float64(v.Float()), 'f', -1, 32)
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64)
	case reflect.Struct:
		buf.WriteRune('{')
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				// reflect says this field is only defined for non-exported fields.
				continue
			}
			if i > 0 {
				buf.WriteRune(',')
			}
			buf.WriteRune('"')
			name := f.Name
			if tag, found := f.Tag.Lookup("json"); found {
				if comma := strings.Index(tag, ","); comma != -1 {
					name = tag[:comma]
				} else {
					name = tag
				}
			}
			buf.WriteString(name)
			buf.WriteRune('"')
			buf.WriteRune(':')
			buf.WriteString(pretty(v.Field(i).Interface()))
		}
		buf.WriteRune('}')
		return buf.String()
	case reflect.Slice, reflect.Array:
		buf.WriteRune('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				buf.WriteRune(',')
			}
			e := v.Index(i)
			buf.WriteString(pretty(e.Interface()))
		}
		buf.WriteRune(']')
		return buf.String()
	case reflect.Map:
		buf.WriteRune('{')
		// This does not sort the map keys, for best perf.
		it := v.MapRange()
		i := 0
		for it.Next() {
			if i > 0 {
				buf.WriteRune(',')
			}
			// JSON only does string keys.
			buf.WriteRune('"')
			buf.WriteString(prettyWithFlags(it.Key().Interface(), flagRawString))
			buf.WriteRune('"')
			buf.WriteRune(':')
			buf.WriteString(pretty(it.Value().Interface()))
			i++
		}
		buf.WriteRune('}')
		return buf.String()
	case reflect.Ptr, reflect.Interface:
		return pretty(v.Elem().Interface())
	}
	return fmt.Sprintf(`"<unhandled-%s>"`, t.Kind().String())
}

type callerID struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

func (l fnlogger) caller() callerID {
	// +1 for this frame, +1 for logr itself.
	// FIXME: Maybe logr should offer a clue as to how many frames are
	// needed here?  Or is it part of the contract to LogSinks?
	_, file, line, ok := runtime.Caller(framesToCaller() + 2)
	if !ok {
		return callerID{"<unknown>", 0}
	}
	return callerID{filepath.Base(file), line}
}

func (l fnlogger) Enabled() bool {
	return l.level == 0
}

func (l fnlogger) Info(msg string, kvList ...interface{}) {
	if l.Enabled() {
		args := make([]interface{}, 0, 64) // using a constant here impacts perf
		if l.logCaller == All || l.logCaller == Info {
			args = append(args, "caller", l.caller())
		}
		args = append(args, "level", l.level, "msg", msg)
		args = append(args, l.values...)
		args = append(args, kvList...)
		argsStr := flatten(args...)
		l.write(l.prefix, argsStr)
	}
}

func (l fnlogger) Error(err error, msg string, kvList ...interface{}) {
	args := make([]interface{}, 0, 64) // using a constant here impacts perf
	if l.logCaller == All || l.logCaller == Error {
		args = append(args, "caller", l.caller())
	}
	args = append(args, "msg", msg)
	var loggableErr interface{}
	if err != nil {
		loggableErr = err.Error()
	}
	args = append(args, "error", loggableErr)
	args = append(args, l.values...)
	args = append(args, kvList...)
	argsStr := flatten(args...)
	l.write(l.prefix, argsStr)
}

func (l fnlogger) V(level int) logr.Logger {
	l.level += level
	return l
}

// WithName returns a new Logger with the specified name appended.  funcr
// uses '/' characters to separate name elements.  Callers should not pass '/'
// in the provided name string, but this library does not actually enforce that.
func (l fnlogger) WithName(name string) logr.Logger {
	if len(l.prefix) > 0 {
		l.prefix = l.prefix + "/"
	}
	l.prefix += name
	return l
}

func (l fnlogger) WithValues(kvList ...interface{}) logr.Logger {
	// Three slice args forces a copy.
	n := len(l.values)
	l.values = append(l.values[:n:n], kvList...)
	return l
}

var _ logr.Logger = fnlogger{}
