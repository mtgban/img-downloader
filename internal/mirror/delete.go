package mirror

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/mtgban/simplecloud"
)

// BundleObjectPath is where one set's bundle of the given hash is stored.
func BundleObjectPath(code, hash string) string {
	return "bundles/" + code + "-" + hash + ".zip"
}

// errNoDelete reports a backend this package has no way to delete through.
var errNoDelete = errors.New("mirror: backend does not support deletion")

// deleteObject removes one object.
//
// simplecloud's interface is reads and writes only, so this reaches past it:
// B2 through the blazer bucket it wraps, the filesystem through os.Remove. The
// path is the same one InitWriter was handed, which is why the leading slash is
// trimmed the way B2Bucket.NewWriter trims it — blazer interpolates the name
// into a URL, where a leading slash would name a different object.
//
// An object that is already gone is a success: the point is that it not be
// there, not that this call be the one to remove it.
func deleteObject(ctx context.Context, bucket simplecloud.ReadWriter, path string) error {
	switch b := bucket.(type) {
	case *simplecloud.B2Bucket:
		err := b.Bucket.Object(strings.TrimLeft(path, "/")).Delete(ctx)
		if err != nil && isNotExist(err) {
			return nil
		}
		return err
	case *simplecloud.FileBucket:
		err := os.Remove(path)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	default:
		return fmt.Errorf("%w: %T", errNoDelete, bucket)
	}
}
