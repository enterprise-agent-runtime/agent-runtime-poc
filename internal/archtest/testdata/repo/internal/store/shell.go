package store

import "golang.org/x/sys/windows"

func shell() {
	_ = windows.ShellExecute(0, nil, nil, nil, nil, 0)
	_ = windows.ShellExecuteEx(nil)
}
