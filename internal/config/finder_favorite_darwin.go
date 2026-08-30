//go:build darwin

package config

import (
	"bytes"
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	sharedFileListFramework = "/System/Library/Frameworks/CoreServices.framework/Frameworks/SharedFileList.framework/SharedFileList"
	coreFoundationFramework = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cfStringEncodingUTF8    = uint32(0x08000100)
	cfURLPOSIXPathStyle     = int32(0)
	sharedFileListNoUI      = uint32(1 << 0)
	sharedFileListNoMount   = uint32(1 << 1)
)

// SharedFileList remains the system interface that writes Finder Favorites.
// Loading it dynamically keeps Config's release binary independent of cgo.
type darwinFinderFavorites struct{}

func newFinderFavoritesStore() finderFavoritesStore { return darwinFinderFavorites{} }

func (darwinFinderFavorites) List() ([]finderFavoriteItem, error) {
	api, err := openFinderFavoritesAPI()
	if err != nil {
		return nil, err
	}
	defer api.close()
	list := api.newList()
	if list == 0 {
		return nil, fmt.Errorf("open Finder Favorites")
	}
	defer api.cfRelease(list)
	return api.items(list)
}

func (darwinFinderFavorites) Add(name, path string) error {
	api, err := openFinderFavoritesAPI()
	if err != nil {
		return err
	}
	defer api.close()
	list := api.newList()
	if list == 0 {
		return fmt.Errorf("open Finder Favorites")
	}
	defer api.cfRelease(list)
	displayName, err := api.newString(name)
	if err != nil {
		return err
	}
	defer api.cfRelease(displayName)
	fileURL, err := api.newFileURL(path)
	if err != nil {
		return err
	}
	defer api.cfRelease(fileURL)
	item := api.insertItemURL(list, api.itemLast, displayName, 0, fileURL, 0, 0)
	if item == 0 {
		return fmt.Errorf("insert Finder Favorite")
	}
	api.cfRelease(item)
	return nil
}

func (darwinFinderFavorites) Remove(expected finderFavoriteItem) error {
	api, err := openFinderFavoritesAPI()
	if err != nil {
		return err
	}
	defer api.close()
	list := api.newList()
	if list == 0 {
		return fmt.Errorf("open Finder Favorites")
	}
	defer api.cfRelease(list)
	snapshot := api.copySnapshot(list, nil)
	if snapshot == 0 {
		return fmt.Errorf("read Finder Favorites")
	}
	defer api.cfRelease(snapshot)
	count := api.cfArrayGetCount(snapshot)
	var found uintptr
	for index := range count {
		item := api.cfArrayGetValueAtIndex(snapshot, index)
		if item != 0 && api.itemGetID(item) == expected.ID {
			if found != 0 {
				return fmt.Errorf("Finder Favorite id %d is ambiguous", expected.ID)
			}
			found = item
		}
	}
	if found == 0 {
		return fmt.Errorf("Finder Favorite id %d disappeared", expected.ID)
	}
	current, err := api.item(found)
	if err != nil {
		return fmt.Errorf("reread Finder Favorite id %d: %w", expected.ID, err)
	}
	if current != expected {
		return fmt.Errorf("Finder Favorite id %d changed before removal", expected.ID)
	}
	if status := api.itemRemove(list, found); status != 0 {
		return fmt.Errorf("remove Finder Favorite id %d: OSStatus %d", expected.ID, status)
	}
	return nil
}

type finderFavoritesAPI struct {
	sharedFileList uintptr
	coreFoundation uintptr
	favoriteItems  uintptr
	itemLast       uintptr

	create                  func(uintptr, uintptr, uintptr) uintptr
	copySnapshot            func(uintptr, *uint32) uintptr
	insertItemURL           func(uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr) uintptr
	itemRemove              func(uintptr, uintptr) int32
	itemGetID               func(uintptr) uint32
	itemCopyDisplayName     func(uintptr) uintptr
	itemCopyResolvedURL     func(uintptr, uint32, *uintptr) uintptr
	cfRelease               func(uintptr)
	cfArrayGetCount         func(uintptr) int
	cfArrayGetValueAtIndex  func(uintptr, int) uintptr
	cfStringCreateWithBytes func(uintptr, *byte, int, uint32, uint8) uintptr
	cfStringGetLength       func(uintptr) int
	cfStringMaximumSize     func(int, uint32) int
	cfStringGetCString      func(uintptr, *byte, int, uint32) uint8
	cfURLCreateFromFilePath func(uintptr, *byte, int, uint8) uintptr
	cfURLCopyFileSystemPath func(uintptr, int32) uintptr
}

func openFinderFavoritesAPI() (*finderFavoritesAPI, error) {
	sharedFileList, err := purego.Dlopen(sharedFileListFramework, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load SharedFileList: %w", err)
	}
	coreFoundation, err := purego.Dlopen(coreFoundationFramework, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		_ = purego.Dlclose(sharedFileList)
		return nil, fmt.Errorf("load CoreFoundation: %w", err)
	}
	api := &finderFavoritesAPI{sharedFileList: sharedFileList, coreFoundation: coreFoundation}
	fail := func(err error) (*finderFavoritesAPI, error) {
		api.close()
		return nil, err
	}
	for _, function := range []struct {
		handle uintptr
		name   string
		to     any
	}{
		{sharedFileList, "LSSharedFileListCreate", &api.create},
		{sharedFileList, "LSSharedFileListCopySnapshot", &api.copySnapshot},
		{sharedFileList, "LSSharedFileListInsertItemURL", &api.insertItemURL},
		{sharedFileList, "LSSharedFileListItemRemove", &api.itemRemove},
		{sharedFileList, "LSSharedFileListItemGetID", &api.itemGetID},
		{sharedFileList, "LSSharedFileListItemCopyDisplayName", &api.itemCopyDisplayName},
		{sharedFileList, "LSSharedFileListItemCopyResolvedURL", &api.itemCopyResolvedURL},
		{coreFoundation, "CFRelease", &api.cfRelease},
		{coreFoundation, "CFArrayGetCount", &api.cfArrayGetCount},
		{coreFoundation, "CFArrayGetValueAtIndex", &api.cfArrayGetValueAtIndex},
		{coreFoundation, "CFStringCreateWithBytes", &api.cfStringCreateWithBytes},
		{coreFoundation, "CFStringGetLength", &api.cfStringGetLength},
		{coreFoundation, "CFStringGetMaximumSizeForEncoding", &api.cfStringMaximumSize},
		{coreFoundation, "CFStringGetCString", &api.cfStringGetCString},
		{coreFoundation, "CFURLCreateFromFileSystemRepresentation", &api.cfURLCreateFromFilePath},
		{coreFoundation, "CFURLCopyFileSystemPath", &api.cfURLCopyFileSystemPath},
	} {
		if err := registerFinderFunction(function.handle, function.name, function.to); err != nil {
			return fail(err)
		}
	}
	favoriteItems, err := finderConstant(sharedFileList, "kLSSharedFileListFavoriteItems")
	if err != nil {
		return fail(err)
	}
	itemLast, err := finderConstant(sharedFileList, "kLSSharedFileListItemLast")
	if err != nil {
		return fail(err)
	}
	api.favoriteItems = favoriteItems
	api.itemLast = itemLast
	return api, nil
}

func registerFinderFunction(handle uintptr, name string, target any) error {
	symbol, err := purego.Dlsym(handle, name)
	if err != nil {
		return fmt.Errorf("load %s: %w", name, err)
	}
	purego.RegisterFunc(target, symbol)
	return nil
}

func finderConstant(handle uintptr, name string) (uintptr, error) {
	symbol, err := purego.Dlsym(handle, name)
	if err != nil {
		return 0, fmt.Errorf("load %s: %w", name, err)
	}
	var copyPointer func(*uintptr, uintptr, uintptr) uintptr
	if err := registerFinderFunction(purego.RTLD_DEFAULT, "memcpy", &copyPointer); err != nil {
		return 0, err
	}
	var value uintptr
	if copyPointer(&value, symbol, unsafe.Sizeof(value)) == 0 {
		return 0, fmt.Errorf("load %s: copy value", name)
	}
	if value == 0 {
		return 0, fmt.Errorf("load %s: value is nil", name)
	}
	return value, nil
}

func (api *finderFavoritesAPI) close() {
	if api.coreFoundation != 0 {
		_ = purego.Dlclose(api.coreFoundation)
		api.coreFoundation = 0
	}
	if api.sharedFileList != 0 {
		_ = purego.Dlclose(api.sharedFileList)
		api.sharedFileList = 0
	}
}

func (api *finderFavoritesAPI) newList() uintptr {
	return api.create(0, api.favoriteItems, 0)
}

func (api *finderFavoritesAPI) items(list uintptr) ([]finderFavoriteItem, error) {
	snapshot := api.copySnapshot(list, nil)
	if snapshot == 0 {
		return nil, fmt.Errorf("read Finder Favorites")
	}
	defer api.cfRelease(snapshot)
	count := api.cfArrayGetCount(snapshot)
	if count < 0 {
		return nil, fmt.Errorf("read Finder Favorites: invalid item count %d", count)
	}
	items := make([]finderFavoriteItem, 0, count)
	for index := range count {
		item := api.cfArrayGetValueAtIndex(snapshot, index)
		if item == 0 {
			return nil, fmt.Errorf("read Finder Favorite %d: item is nil", index)
		}
		favorite, err := api.item(item)
		if err != nil {
			return nil, fmt.Errorf("read Finder Favorite %d: %w", index, err)
		}
		items = append(items, favorite)
	}
	return items, nil
}

func (api *finderFavoritesAPI) item(item uintptr) (finderFavoriteItem, error) {
	nameRef := api.itemCopyDisplayName(item)
	if nameRef == 0 {
		return finderFavoriteItem{}, fmt.Errorf("name is unavailable")
	}
	name, err := api.goString(nameRef)
	api.cfRelease(nameRef)
	if err != nil {
		return finderFavoriteItem{}, fmt.Errorf("read name: %w", err)
	}
	path := ""
	urlRef := api.itemCopyResolvedURL(item, sharedFileListNoUI|sharedFileListNoMount, nil)
	if urlRef != 0 {
		pathRef := api.cfURLCopyFileSystemPath(urlRef, cfURLPOSIXPathStyle)
		api.cfRelease(urlRef)
		if pathRef != 0 {
			path, err = api.goString(pathRef)
			api.cfRelease(pathRef)
			if err != nil {
				return finderFavoriteItem{}, fmt.Errorf("read %q path: %w", name, err)
			}
		}
	}
	return finderFavoriteItem{ID: api.itemGetID(item), Name: name, Path: path}, nil
}

func (api *finderFavoritesAPI) newString(value string) (uintptr, error) {
	raw := []byte(value)
	if len(raw) == 0 {
		return 0, fmt.Errorf("create empty Core Foundation string")
	}
	result := api.cfStringCreateWithBytes(0, &raw[0], len(raw), cfStringEncodingUTF8, 0)
	if result == 0 {
		return 0, fmt.Errorf("create Core Foundation string")
	}
	return result, nil
}

func (api *finderFavoritesAPI) goString(value uintptr) (string, error) {
	length := api.cfStringGetLength(value)
	maximum := api.cfStringMaximumSize(length, cfStringEncodingUTF8)
	if length < 0 || maximum < 0 || maximum > 16*1024*1024 {
		return "", fmt.Errorf("invalid Core Foundation string length")
	}
	buffer := make([]byte, maximum+1)
	if api.cfStringGetCString(value, &buffer[0], len(buffer), cfStringEncodingUTF8) == 0 {
		return "", fmt.Errorf("decode Core Foundation string")
	}
	terminator := bytes.IndexByte(buffer, 0)
	if terminator < 0 {
		return "", fmt.Errorf("decode unterminated Core Foundation string")
	}
	return string(buffer[:terminator]), nil
}

func (api *finderFavoritesAPI) newFileURL(path string) (uintptr, error) {
	raw := []byte(path)
	if len(raw) == 0 {
		return 0, fmt.Errorf("create file URL for empty path")
	}
	result := api.cfURLCreateFromFilePath(0, &raw[0], len(raw), 1)
	if result == 0 {
		return 0, fmt.Errorf("create file URL")
	}
	return result, nil
}
