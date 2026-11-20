package mac

import (
	"context"
	"runtime"
)

func probeWorker(ctx context.Context, pid uint32, home string, requests <-chan uint64, out chan<- observation) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	db := catalogDB{}
	defer db.close()
	v := locator{app: native.axApplication(int32(pid)), ax: darwinAX{}, lookup: db.lookup}
	defer v.close()
	for {
		select {
		case <-ctx.Done():
			return
		case epoch := <-requests:
			p := pool()
			o := v.probe(ctx, home)
			o.Epoch = epoch
			p.Send(sel("drain"))
			select {
			case out <- o:
			case <-ctx.Done():
				if o.Window != 0 {
					native.release(o.Window)
				}
				return
			}
		}
	}
}
