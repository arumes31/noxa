//go:build windows

package wintoast

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Interface IDs from the Windows SDK (windows.ui.notifications.h,
// windows.data.xml.dom.h). Vtable slots below follow those headers; slot 6
// is the first method after IInspectable.
var (
	iidToastNotificationManagerStatics  = ole.NewGUID("{50ac103f-d235-4598-bbef-98fe4d1a3ad4}")
	iidToastNotificationManagerStatics2 = ole.NewGUID("{7ab93c52-0e48-4750-ba9d-1a4113981847}")
	iidToastNotificationFactory         = ole.NewGUID("{04124b20-82c6-4229-b109-fd9ed4662b53}")
	iidToastNotification2               = ole.NewGUID("{9dfb9fd1-143a-490e-90bf-b9fba7132de7}")
	iidXMLDocument                      = ole.NewGUID("{f7f3a506-1e87-42d6-bcfb-b8c809fa5494}")
	iidXMLDocumentIO                    = ole.NewGUID("{6cd0e74e-ee65-4489-9ebf-ca43e87ba637}")
)

const (
	slotCreateToastNotifierWithID = 7  // IToastNotificationManagerStatics
	slotGetHistory                = 6  // IToastNotificationManagerStatics2
	slotRemoveGroupedTagWithID    = 8  // IToastNotificationHistory
	slotClearWithID               = 12 // IToastNotificationHistory
	slotCreateToastNotification   = 6  // IToastNotificationFactory
	slotPutTag                    = 6  // IToastNotification2
	slotPutGroup                  = 8  // IToastNotification2
	slotShow                      = 6  // IToastNotifier
	slotLoadXML                   = 6  // IXmlDocumentIO
)

var (
	modcombase              = windows.NewLazySystemDLL("combase.dll")
	procRegisterClassObject = modcombase.NewProc("CoRegisterClassObject")

	callbackMu sync.RWMutex
	callback   Callback = func(string, string, []UserData) {}

	worker struct {
		once  sync.Once
		calls chan func()
		err   error
	}
	activatorCLSID *ole.GUID
)

// SetCallback installs the activation handler.
func SetCallback(cb Callback) {
	callbackMu.Lock()
	defer callbackMu.Unlock()
	if cb == nil {
		cb = func(string, string, []UserData) {}
	}
	callback = cb
}

func currentCallback() Callback {
	callbackMu.RLock()
	defer callbackMu.RUnlock()
	return callback
}

// Register writes the per-user registration Windows needs to show toasts for
// an unpackaged app and to route activations back to it (HKCU only), and
// starts the COM thread that receives activations. Call it once at startup,
// before Show; a second call with the same data is cheap.
func Register(data AppData) error {
	if data.AppID == "" || data.CLSID == "" || data.Executable == "" {
		return errors.New("wintoast: AppID, CLSID and Executable are required")
	}
	clsid, err := windows.GUIDFromString(data.CLSID)
	if err != nil {
		return fmt.Errorf("wintoast: CLSID: %w", err)
	}
	name := data.DisplayName
	if name == "" {
		name = data.AppID
	}
	appKey := `Software\Classes\AppUserModelId\` + data.AppID
	values := map[string]string{"DisplayName": name, "CustomActivator": data.CLSID}
	if data.IconPath != "" {
		values["IconUri"] = data.IconPath
	}
	if err := writeStrings(appKey, values); err != nil {
		return err
	}
	server := `Software\Classes\CLSID\` + data.CLSID + `\LocalServer32`
	if err := writeStrings(server, map[string]string{"": `"` + data.Executable + `"`}); err != nil {
		return err
	}
	activatorCLSID = ole.NewGUID(clsid.String())
	return start()
}

func writeStrings(path string, values map[string]string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("wintoast: opening HKCU\\%s: %w", path, err)
	}
	defer func() { _ = key.Close() }()
	for name, value := range values {
		if err := key.SetStringValue(name, value); err != nil {
			return fmt.Errorf("wintoast: writing HKCU\\%s\\%s: %w", path, name, err)
		}
	}
	return nil
}

// start runs one OS thread in the multithreaded apartment for every WinRT
// call and registers the activator class there for the whole process.
func start() error {
	worker.once.Do(func() {
		ready := make(chan error, 1)
		worker.calls = make(chan func(), 16)
		go func() {
			runtime.LockOSThread()
			if err := ole.RoInitialize(1); err != nil { // RO_INIT_MULTITHREADED
				ready <- fmt.Errorf("wintoast: RoInitialize: %w", err)
				return
			}
			var cookie uint32
			hr, _, _ := procRegisterClassObject.Call(
				uintptr(unsafe.Pointer(activatorCLSID)),
				uintptr(unsafe.Pointer(classFactory)),
				uintptr(ole.CLSCTX_LOCAL_SERVER),
				1, // REGCLS_MULTIPLEUSE
				uintptr(unsafe.Pointer(&cookie)),
			)
			if hr != 0 {
				ready <- fmt.Errorf("wintoast: CoRegisterClassObject: %w", ole.NewError(hr))
				return
			}
			ready <- nil
			for call := range worker.calls {
				call()
			}
		}()
		worker.err = <-ready
	})
	return worker.err
}

// run executes fn on the COM thread and returns its error.
func run(fn func() error) error {
	if worker.calls == nil || worker.err != nil {
		if worker.err != nil {
			return worker.err
		}
		return errors.New("wintoast: not registered")
	}
	done := make(chan error, 1)
	worker.calls <- func() {
		defer func() {
			if v := recover(); v != nil {
				done <- fmt.Errorf("wintoast: %v", v)
			}
		}()
		done <- fn()
	}
	return <-done
}

// Show displays a toast for appID, replacing an earlier toast with the same
// tag and group.
func Show(appID string, toast Toast) error {
	return run(func() error {
		doc, err := loadXML(toast.XML)
		if err != nil {
			return err
		}
		defer doc.Release()
		factory, err := activationFactory("Windows.UI.Notifications.ToastNotification", iidToastNotificationFactory)
		if err != nil {
			return err
		}
		defer factory.Release()
		var notification *ole.IUnknown
		if err := call(factory, slotCreateToastNotification, uintptr(unsafe.Pointer(doc)), uintptr(unsafe.Pointer(&notification))); err != nil {
			return fmt.Errorf("wintoast: CreateToastNotification: %w", err)
		}
		defer notification.Release()
		if toast.Tag != "" || toast.Group != "" {
			second, err := notification.QueryInterface(iidToastNotification2)
			if err != nil {
				return fmt.Errorf("wintoast: IToastNotification2: %w", err)
			}
			defer second.Release()
			if err := withHString(toast.Tag, func(tag ole.HString) error {
				return call(&second.IUnknown, slotPutTag, uintptr(tag))
			}); err != nil {
				return fmt.Errorf("wintoast: put_Tag: %w", err)
			}
			if err := withHString(toast.Group, func(group ole.HString) error {
				return call(&second.IUnknown, slotPutGroup, uintptr(group))
			}); err != nil {
				return fmt.Errorf("wintoast: put_Group: %w", err)
			}
		}
		manager, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", iidToastNotificationManagerStatics)
		if err != nil {
			return err
		}
		defer manager.Release()
		var notifier *ole.IUnknown
		if err := withHString(appID, func(id ole.HString) error {
			return call(manager, slotCreateToastNotifierWithID, uintptr(id), uintptr(unsafe.Pointer(&notifier)))
		}); err != nil {
			return fmt.Errorf("wintoast: CreateToastNotifierWithId: %w", err)
		}
		defer notifier.Release()
		if err := call(notifier, slotShow, uintptr(unsafe.Pointer(notification))); err != nil {
			return fmt.Errorf("wintoast: Show: %w", err)
		}
		return nil
	})
}

// Remove deletes the toast with this tag and group from Action Center.
func Remove(appID, group, tag string) error {
	return withHistory(func(history *ole.IUnknown) error {
		return withHString(tag, func(t ole.HString) error {
			return withHString(group, func(g ole.HString) error {
				return withHString(appID, func(id ole.HString) error {
					return call(history, slotRemoveGroupedTagWithID, uintptr(t), uintptr(g), uintptr(id))
				})
			})
		})
	})
}

// Clear deletes every toast of appID from Action Center.
func Clear(appID string) error {
	return withHistory(func(history *ole.IUnknown) error {
		return withHString(appID, func(id ole.HString) error {
			return call(history, slotClearWithID, uintptr(id))
		})
	})
}

func withHistory(fn func(history *ole.IUnknown) error) error {
	return run(func() error {
		statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", iidToastNotificationManagerStatics2)
		if err != nil {
			return err
		}
		defer statics.Release()
		var history *ole.IUnknown
		if err := call(statics, slotGetHistory, uintptr(unsafe.Pointer(&history))); err != nil {
			return fmt.Errorf("wintoast: get_History: %w", err)
		}
		defer history.Release()
		return fn(history)
	})
}

func loadXML(content string) (*ole.IUnknown, error) {
	instance, err := ole.RoActivateInstance("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return nil, fmt.Errorf("wintoast: XmlDocument: %w", err)
	}
	defer instance.Release()
	io, err := instance.QueryInterface(iidXMLDocumentIO)
	if err != nil {
		return nil, fmt.Errorf("wintoast: IXmlDocumentIO: %w", err)
	}
	defer io.Release()
	if err := withHString(content, func(text ole.HString) error {
		return call(&io.IUnknown, slotLoadXML, uintptr(text))
	}); err != nil {
		return nil, fmt.Errorf("wintoast: LoadXml: %w", err)
	}
	doc, err := instance.QueryInterface(iidXMLDocument)
	if err != nil {
		return nil, fmt.Errorf("wintoast: IXmlDocument: %w", err)
	}
	return &doc.IUnknown, nil
}

func activationFactory(class string, iid *ole.GUID) (*ole.IUnknown, error) {
	inspectable, err := ole.RoGetActivationFactory(class, iid)
	if err != nil {
		return nil, fmt.Errorf("wintoast: activation factory %s: %w", class, err)
	}
	return &inspectable.IUnknown, nil
}

// call invokes vtable slot on object, passing object as `this`.
func call(object *ole.IUnknown, slot int, args ...uintptr) error {
	vtable := unsafe.Slice((*uintptr)(unsafe.Pointer(object.RawVTable)), slot+1)
	hr, _, _ := syscall.SyscallN(vtable[slot], append([]uintptr{uintptr(unsafe.Pointer(object))}, args...)...)
	if hr != 0 {
		return ole.NewError(hr)
	}
	return nil
}

func withHString(value string, fn func(ole.HString) error) error {
	h, err := ole.NewHString(value)
	if err != nil {
		return err
	}
	defer func() { _ = ole.DeleteHString(h) }()
	return fn(h)
}
