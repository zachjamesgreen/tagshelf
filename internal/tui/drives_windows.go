package tui

import "golang.org/x/sys/windows"

// drives lists the top folder of every drive Windows has a letter for, such
// as C:\, without opening any of them.
func drives() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []string
	for i := range 26 {
		if mask&(1<<i) != 0 {
			out = append(out, string(rune('A'+i))+`:\`)
		}
	}
	return out
}
