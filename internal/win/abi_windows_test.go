package win

import "unsafe"

var _ [40 - int(unsafe.Sizeof(uiaSink{}))]byte
var _ [int(unsafe.Sizeof(uiaSink{})) - 40]byte

// Both directions must be non-negative. These checks run at compile time even
// when cross-compiling, pinning the Windows x64 struct layouts used by syscalls.
var _ [80 - int(unsafe.Sizeof(wndClass{}))]byte
var _ [int(unsafe.Sizeof(wndClass{})) - 80]byte
var _ [48 - int(unsafe.Sizeof(msg{}))]byte
var _ [int(unsafe.Sizeof(msg{})) - 48]byte
var _ [80 - int(unsafe.Sizeof(memoryCounters{}))]byte
var _ [int(unsafe.Sizeof(memoryCounters{})) - 80]byte
var _ [24 - int(unsafe.Sizeof(variant{}))]byte
var _ [int(unsafe.Sizeof(variant{})) - 24]byte
var _ [140 - int(unsafe.Sizeof(titlebarInfoEx{}))]byte
var _ [int(unsafe.Sizeof(titlebarInfoEx{})) - 140]byte
var _ [72 - int(unsafe.Sizeof(guiThreadInfo{}))]byte
var _ [int(unsafe.Sizeof(guiThreadInfo{})) - 72]byte
var _ [72 - int(unsafe.Sizeof(paintStruct{}))]byte
var _ [int(unsafe.Sizeof(paintStruct{})) - 72]byte
var _ [36 - int(unsafe.Sizeof(dwriteMetrics{}))]byte
var _ [int(unsafe.Sizeof(dwriteMetrics{})) - 36]byte
var _ [28 - int(unsafe.Sizeof(d2dTargetProperties{}))]byte
var _ [int(unsafe.Sizeof(d2dTargetProperties{})) - 28]byte
var _ [24 - int(unsafe.Sizeof(d2dRoundedRect{}))]byte
var _ [int(unsafe.Sizeof(d2dRoundedRect{})) - 24]byte
