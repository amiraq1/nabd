//go:build unix && !linux

package tools

import (
	"fmt"
	"os"
	"reflect"
)

func ensureNoHardLinks(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	value := reflect.ValueOf(info.Sys())
	if value.IsValid() && value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		for _, name := range []string{"Nlink", "NumberOfLinks"} {
			field := value.FieldByName(name)
			if field.IsValid() {
				switch field.Kind() {
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					if field.Uint() > 1 {
						return fmt.Errorf("refusing multiply-linked file %s (link count %d)", info.Name(), field.Uint())
					}
					return nil
				}
			}
		}
	}
	return fmt.Errorf("cannot verify hard-link count for %s", info.Name())
}
