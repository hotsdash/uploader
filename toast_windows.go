//go:build windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Notifications through WinRT: load the toast XML into an XmlDocument, make a ToastNotification of it and show it
// with the notifier for our app ID. Clicking one with a web address opens it in the browser (the XML says so,
// notify.go), with nothing of ours having to be running.

var (
	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoUninitialize         = combase.NewProc("RoUninitialize")
	procRoActivateInstance     = combase.NewProc("RoActivateInstance")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")

	iidToastNotificationManagerStatics = mustGUID("50ac103f-d235-4598-bbef-98fe4d1a3ad4")
	iidToastNotificationFactory        = mustGUID("04124b20-82c6-4229-b109-fd9ed4662b53")
	iidXMLDocument                     = mustGUID("f7f3a506-1e87-42d6-bcfb-b8c809fa5494")
	iidXMLDocumentIO                   = mustGUID("6cd0e74e-ee65-4489-9ebf-ca43e87ba637")
)

const (
	roInitMultithreaded = 1
	// positions in the method tables (the first six of a WinRT object are IUnknown's and IInspectable's)
	xmlDocumentIOLoadXML           = 6
	toastFactoryCreate             = 6
	toastManagerCreateNotifierByID = 7
	toastNotifierShow              = 6
)

// toastFailed hears why a notification couldn't be shown (the log).
var toastFailed = func(error) {}

// toast shows a Windows notification. Clicking it opens open, when that's a web address.
func toast(title, message, open string) {
	x := toastXML(title, message, open)
	go func() {
		if err := registerAppID(); err != nil {
			toastFailed(err) // the Start menu shortcut carries the ID too: try anyway
		}
		err := withToast(x, func(notifier, notification *comObject) error {
			return notifier.call(toastNotifierShow, uintptr(unsafe.Pointer(notification)))
		})
		if err != nil {
			toastFailed(err)
		}
	}()
}

// registerAppID tells Windows the name and icon that go with our app ID. The Start menu shortcut carries the ID as
// well, but after an update from an earlier version the first notification can come before it has been rewritten.
func registerAppID() error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appUserModelID, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	icon := filepath.Join(filepath.Dir(installPath()), "hots-dash.ico")
	if _, err := os.Stat(icon); err == nil {
		if err := k.SetStringValue("IconUri", icon); err != nil {
			return err
		}
	}
	return k.SetStringValue("DisplayName", appName)
}

// hstring is a WinRT string. The caller frees it with free.
type hstring uintptr

func newHString(s string) (hstring, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h hstring
	hr, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	return h, hresult("WindowsCreateString", hr)
}

func (h hstring) free() { _, _, _ = procWindowsDeleteString.Call(uintptr(h)) }

// activationFactory is the object a WinRT class's static methods and constructors are on.
func activationFactory(class string, iid guid) (*comObject, error) {
	name, err := newHString(class)
	if err != nil {
		return nil, err
	}
	defer name.free()
	var out *comObject
	hr, _, _ := procRoGetActivationFactory.Call(uintptr(name), uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out)))
	return out, hresult(class, hr)
}

// xmlDocument is a Windows.Data.Xml.Dom.XmlDocument holding x, as IXmlDocument.
func xmlDocument(x string) (*comObject, error) {
	name, err := newHString("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return nil, err
	}
	defer name.free()
	text, err := newHString(x)
	if err != nil {
		return nil, err
	}
	defer text.free()
	var instance *comObject
	hr, _, _ := procRoActivateInstance.Call(uintptr(name), uintptr(unsafe.Pointer(&instance)))
	if err := hresult("XmlDocument", hr); err != nil {
		return nil, err
	}
	defer instance.release()
	loader, err := instance.query(iidXMLDocumentIO)
	if err != nil {
		return nil, err
	}
	defer loader.release()
	if err := loader.call(xmlDocumentIOLoadXML, uintptr(text)); err != nil {
		return nil, err
	}
	return instance.query(iidXMLDocument)
}

// withToast makes the notification for the toast XML x and its notifier, and hands them to f.
func withToast(x string, f func(notifier, notification *comObject) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procRoInitialize.Call(roInitMultithreaded)
	if err := hresult("RoInitialize", hr); err != nil {
		return err
	}
	defer procRoUninitialize.Call()

	doc, err := xmlDocument(x)
	if err != nil {
		return err
	}
	defer doc.release()
	factory, err := activationFactory("Windows.UI.Notifications.ToastNotification", iidToastNotificationFactory)
	if err != nil {
		return err
	}
	defer factory.release()
	var notification *comObject
	if err := factory.call(toastFactoryCreate, uintptr(unsafe.Pointer(doc)), uintptr(unsafe.Pointer(&notification))); err != nil {
		return err
	}
	defer notification.release()

	manager, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", iidToastNotificationManagerStatics)
	if err != nil {
		return err
	}
	defer manager.release()
	id, err := newHString(appUserModelID)
	if err != nil {
		return err
	}
	defer id.free()
	var notifier *comObject
	if err := manager.call(toastManagerCreateNotifierByID, uintptr(id), uintptr(unsafe.Pointer(&notifier))); err != nil {
		return err
	}
	defer notifier.release()
	return f(notifier, notification)
}
