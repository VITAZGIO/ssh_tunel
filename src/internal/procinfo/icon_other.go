//go:build !windows

package procinfo

import "errors"

// IconPNG — значки программ достаются только на Windows (там же и список
// запущенных программ, List(), работает только для неё). На остальных
// системах спрашивать нечего.
func IconPNG(path string) ([]byte, error) {
	return nil, errors.New("иконки программ поддержаны только на Windows")
}
