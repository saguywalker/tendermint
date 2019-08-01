package rpcserver

import (
	"encoding/hex"
	"net/http"
	"reflect"
	"strings"

	"github.com/pkg/errors"

	amino "github.com/tendermint/go-amino"

	"github.com/tendermint/tendermint/libs/log"
	types "github.com/tendermint/tendermint/rpc/lib/types"
)

///////////////////////////////////////////////////////////////////////////////
// HTTP + URI handler
///////////////////////////////////////////////////////////////////////////////

// convert from a function name to the http handler
func makeHTTPHandler(rpcFunc *RPCFunc, cdc *amino.Codec, logger log.Logger) func(http.ResponseWriter, *http.Request) {
	// Always return -1 as there's no ID here.
	dummyID := types.JSONRPCIntID(-1) // URIClientRequestID

	// Exception for websocket endpoints
	if rpcFunc.ws {
		return func(w http.ResponseWriter, r *http.Request) {
			WriteRPCResponseHTTP(w, types.RPCMethodNotFoundError(dummyID))
		}
	}

	// All other endpoints
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("HTTP HANDLER", "req", r)

		ctx := &types.Context{HTTPReq: r}
		args := []reflect.Value{reflect.ValueOf(ctx)}

		fnArgs, err := httpParamsToArgs(rpcFunc, cdc, r)
		if err != nil {
			WriteRPCResponseHTTP(w, types.RPCInvalidParamsError(dummyID, errors.Wrap(err, "error converting http params to arguments")))
			return
		}
		args = append(args, fnArgs...)

		returns := rpcFunc.f.Call(args)

		logger.Info("HTTPRestRPC", "method", r.URL.Path, "args", args, "returns", returns)
		result, err := unreflectResult(returns)
		if err != nil {
			WriteRPCResponseHTTP(w, types.RPCInternalError(dummyID, err))
			return
		}
		WriteRPCResponseHTTP(w, types.NewRPCSuccessResponse(cdc, dummyID, result))
	}
}

// Covert an http query to a list of properly typed values.
// To be properly decoded the arg must be a concrete type from tendermint (if its an interface).
func httpParamsToArgs(rpcFunc *RPCFunc, cdc *amino.Codec, r *http.Request) ([]reflect.Value, error) {
	// skip types.Context
	const argsOffset = 1

	values := make([]reflect.Value, len(rpcFunc.argNames))

	for i, name := range rpcFunc.argNames {
		argType := rpcFunc.args[i+argsOffset]

		values[i] = reflect.Zero(argType) // set default for that type

		arg := GetParam(r, name)
		// log.Notice("param to arg", "argType", argType, "name", name, "arg", arg)

		if "" == arg {
			continue
		}

		v, err, ok := nonJSONStringToArg(cdc, argType, arg)
		if err != nil {
			return nil, err
		}
		if ok {
			values[i] = v
			continue
		}

		values[i], err = jsonStringToArg(cdc, argType, arg)
		if err != nil {
			return nil, err
		}
	}

	return values, nil
}

func jsonStringToArg(cdc *amino.Codec, rt reflect.Type, arg string) (reflect.Value, error) {
	rv := reflect.New(rt)
	err := cdc.UnmarshalJSON([]byte(arg), rv.Interface())
	if err != nil {
		return rv, err
	}
	rv = rv.Elem()
	return rv, nil
}

func nonJSONStringToArg(cdc *amino.Codec, rt reflect.Type, arg string) (reflect.Value, error, bool) {
	if rt.Kind() == reflect.Ptr {
		rv_, err, ok := nonJSONStringToArg(cdc, rt.Elem(), arg)
		if err != nil {
			return reflect.Value{}, err, false
		} else if ok {
			rv := reflect.New(rt.Elem())
			rv.Elem().Set(rv_)
			return rv, nil, true
		} else {
			return reflect.Value{}, nil, false
		}
	} else {
		return _nonJSONStringToArg(cdc, rt, arg)
	}
}

// NOTE: rt.Kind() isn't a pointer.
func _nonJSONStringToArg(cdc *amino.Codec, rt reflect.Type, arg string) (reflect.Value, error, bool) {
	isIntString := RE_INT.Match([]byte(arg))
	isQuotedString := strings.HasPrefix(arg, `"`) && strings.HasSuffix(arg, `"`)
	isHexString := strings.HasPrefix(strings.ToLower(arg), "0x")

	var expectingString, expectingByteSlice, expectingInt bool
	switch rt.Kind() {
	case reflect.Int, reflect.Uint, reflect.Int8, reflect.Uint8, reflect.Int16, reflect.Uint16, reflect.Int32, reflect.Uint32, reflect.Int64, reflect.Uint64:
		expectingInt = true
	case reflect.String:
		expectingString = true
	case reflect.Slice:
		expectingByteSlice = rt.Elem().Kind() == reflect.Uint8
	}

	if isIntString && expectingInt {
		qarg := `"` + arg + `"`
		// jsonStringToArg
		rv, err := jsonStringToArg(cdc, rt, qarg)
		if err != nil {
			return rv, err, false
		}
		return rv, nil, true
	}

	if isHexString {
		if !expectingString && !expectingByteSlice {
			err := errors.Errorf("got a hex string arg, but expected '%s'",
				rt.Kind().String())
			return reflect.ValueOf(nil), err, false
		}

		var value []byte
		value, err := hex.DecodeString(arg[2:])
		if err != nil {
			return reflect.ValueOf(nil), err, false
		}
		if rt.Kind() == reflect.String {
			return reflect.ValueOf(string(value)), nil, true
		}
		return reflect.ValueOf([]byte(value)), nil, true
	}

	if isQuotedString && expectingByteSlice {
		v := reflect.New(reflect.TypeOf(""))
		err := cdc.UnmarshalJSON([]byte(arg), v.Interface())
		if err != nil {
			return reflect.ValueOf(nil), err, false
		}
		v = v.Elem()
		return reflect.ValueOf([]byte(v.String())), nil, true
	}

	return reflect.ValueOf(nil), nil, false
}
