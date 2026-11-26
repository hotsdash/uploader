//go:build windows

package winshell

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Just enough COM to make the Start menu shortcut and show notifications. An object is a pointer to its table of
// methods, called by their position in it, as the Windows headers (shobjidl.h and friends) declare them. The first
// three are always QueryInterface, AddRef and Release.

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
)

type comObject struct{ methods *[32]uintptr }

// hresult: negative is a failure.
func hresult(what string, hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s: 0x%08x", what, uint32(hr))
	}
	return nil
}

// call runs method n of the object. Pointers must be turned into uintptr in the call itself: the directive below
// then keeps what they point to alive and in place until it returns.
//
//go:uintptrescapes
func (o *comObject) call(n int, args ...uintptr) error {
	hr, _, _ := syscall.SyscallN(o.methods[n], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return hresult(fmt.Sprintf("COM method %d", n), hr)
}

func (o *comObject) release() {
	_, _, _ = syscall.SyscallN(o.methods[2], uintptr(unsafe.Pointer(o)))
}

// query asks the object for another of its interfaces (QueryInterface). The caller releases it.
func (o *comObject) query(iid guid) (*comObject, error) {
	var out *comObject
	err := o.call(0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out)))
	return out, err
}

func createInstance(clsid, iid guid) (*comObject, error) {
	var out *comObject
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsid)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out)))
	return out, hresult("CoCreateInstance", hr)
}

// inApartment runs f with COM set up, on one OS thread.
func inApartment(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if err := hresult("CoInitializeEx", hr); err != nil {
		return err
	}
	defer procCoUninitialize.Call()
	return f()
}
