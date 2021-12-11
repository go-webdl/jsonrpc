package jsonrpc

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	ErrHandlerNotExported = errors.New("method handler is not exported")
	ErrHandlerSignature   = errors.New("method handler must has signature func(*http.Request, <*Args>, <*Reply>) error")
)

var (
	// Precompute the reflect.Type of error and http.Request
	typeOfError   = reflect.TypeOf((*error)(nil)).Elem()
	typeOfRequest = reflect.TypeOf((*http.Request)(nil)).Elem()
	nilErrorValue = reflect.Zero(reflect.TypeOf((*error)(nil)).Elem())
)

// type Method func(id, method string, params []json.RawMessage) (statusCode int, result interface{}, err *Error)

type Server struct {
	sync.Mutex
	methods map[string]*methodSpec
}

type methodSpec struct {
	method    reflect.Value // receiver method
	argsType  reflect.Type  // type of the request argument
	replyType reflect.Type  // type of the response argument
}

func (s *Server) Register(method string, handler interface{}) (err error) {
	vMethod := reflect.ValueOf(handler)
	tMethod := vMethod.Type()

	if tMethod.PkgPath() != "" {
		err = ErrHandlerNotExported
		return
	}

	// Handler needs three inputs: *http.Request, *args, *reply.
	if tMethod.NumIn() != 3 {
		err = ErrHandlerSignature
		return
	}

	// First argument must be a pointer and must be http.Request.
	reqType := tMethod.In(0)
	if reqType.Kind() != reflect.Ptr || reqType.Elem() != typeOfRequest {
		err = ErrHandlerSignature
		return
	}

	// Second argument must be a pointer and must be exported.
	args := tMethod.In(1)
	if args.Kind() != reflect.Ptr || !isExportedOrBuiltin(args) {
		err = ErrHandlerSignature
		return
	}

	// Third argument must be a pointer and must be exported.
	reply := tMethod.In(2)
	if reply.Kind() != reflect.Ptr || !isExportedOrBuiltin(reply) {
		err = ErrHandlerSignature
		return
	}

	// Method needs one out: error.
	if tMethod.NumOut() != 1 {
		err = ErrHandlerSignature
		return
	}
	if returnType := tMethod.Out(0); returnType != typeOfError {
		err = ErrHandlerSignature
		return
	}

	// Add to the map.
	s.Lock()
	defer s.Unlock()
	if s.methods == nil {
		s.methods = make(map[string]*methodSpec)
	} else if _, ok := s.methods[method]; ok {
		return fmt.Errorf("rpc: method already defined: %s", method)
	}
	s.methods[method] = &methodSpec{
		method:    vMethod,
		argsType:  args.Elem(),
		replyType: reply.Elem(),
	}
	return
}

// get returns a registered method given the method's name.
func (s *Server) get(method string) (methodSpec *methodSpec, err error) {
	s.Lock()
	methodSpec = s.methods[method]
	s.Unlock()
	if methodSpec == nil {
		err = fmt.Errorf("rpc: can't find method %q", method)
	}
	return
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		WriteError(w, http.StatusMethodNotAllowed, "rpc: POST method required, received "+r.Method)
		return
	}

	codec := NewCodec()

	// Create a new codec request.
	codecReq := codec.NewRequest(r)

	// Get service method to be called.
	method, errMethod := codecReq.Method()
	if errMethod != nil {
		codecReq.WriteError(w, http.StatusBadRequest, errMethod)
		return
	}

	methodSpec, errGet := s.get(method)
	if errGet != nil {
		codecReq.WriteError(w, http.StatusBadRequest, errGet)
		return
	}

	// Decode the args
	args := reflect.New(methodSpec.argsType)
	if errRead := codecReq.ReadRequest(args.Interface()); errRead != nil {
		codecReq.WriteError(w, http.StatusBadRequest, errRead)
		return
	}

	// Prepare the reply
	reply := reflect.New(methodSpec.replyType)

	errValue := methodSpec.method.Call([]reflect.Value{
		reflect.ValueOf(r),
		args,
		reply,
	})

	// Extract the result to error if needed.
	var errResult error
	statusCode := http.StatusOK
	errInter := errValue[0].Interface()
	if errInter != nil {
		statusCode = http.StatusBadRequest
		errResult = errInter.(error)
	}

	// Prevents Internet Explorer from MIME-sniffing a response away
	// from the declared content-type
	w.Header().Set("x-content-type-options", "nosniff")

	// Encode the response.
	if errResult == nil {
		codecReq.WriteResponse(w, reply.Interface())
	} else {
		codecReq.WriteError(w, statusCode, errResult)
	}
}

// isExported returns true of a string is an exported (upper case) name.
func isExported(name string) bool {
	rune, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(rune)
}

// isExportedOrBuiltin returns true if a type is exported or a builtin.
func isExportedOrBuiltin(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// PkgPath will be non-empty even for an exported type,
	// so we need to check the type name as well.
	return isExported(t.Name()) || t.PkgPath() == ""
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprint(w, msg)
}
