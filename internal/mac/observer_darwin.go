package mac

type darwinObserver struct {
	darwinAX
	pid uint32
}

func (b darwinObserver) create() uintptr {
	if !native.axTrusted() {
		return 0
	}
	var observer uintptr
	if native.axObserver(int32(b.pid), axEventCallback, &observer) != 0 || observer == 0 {
		return 0
	}
	native.addSource(native.runLoop(), native.axObserverSource(observer), native.defaultMode)
	return observer
}
func (darwinObserver) close(observer uintptr) {
	native.removeSource(native.runLoop(), native.axObserverSource(observer), native.defaultMode)
	native.release(observer)
}
func (b darwinObserver) application() uintptr { return native.axApplication(int32(b.pid)) }
func (darwinObserver) subscribe(observer, el uintptr, name string) int32 {
	return native.axObserve(observer, el, uintptr(ns(name)), 0)
}
func (darwinObserver) unsubscribe(observer, el uintptr, name string) {
	native.axUnobserve(observer, el, uintptr(ns(name)))
}
