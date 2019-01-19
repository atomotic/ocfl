package file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/birkland/ocfl"
	"github.com/birkland/ocfl/metadata"
	"github.com/birkland/ocfl/resolv"
	"github.com/karrick/godirwalk"
	"github.com/pkg/errors"
)

const (
	dontGoDeeper = true
	goDeeper     = false
)

// Scope defines a bounded set of OCFL entries (e.g. everything under a given root)
type scope struct {
	root      *resolv.EntityRef
	startFrom *resolv.EntityRef
	desired   *resolv.EntityRef
}

// NewScope defines a scope for ocfl entities underneath the given parent entity
// Logical choices for a parent include an OCFL root, an ocfl object, or
// an ocfl version.
func newScope(under *resolv.EntityRef, t ocfl.Type) (*scope, error) {
	root, err := findRoot(under, ocfl.Root)
	if err != nil {
		return nil, err
	}

	desired := &resolv.EntityRef{Type: t}
	if under.Type == t {
		desired = under
	}

	return &scope{
		root:      root,
		startFrom: under,
		desired:   desired,
	}, nil
}

// Walk iterates through in-scope OCFL entities.
// Uses a two-step algorithm for iterating entities:
// (a) when starting from an ocfl root or intermediate node, walk directories until an object root is found
// (b) walk the entities in an object (versions, files) using data from the manifest rather than the filesystem
//
// TODO: make this parallel!
func (s *scope) walk(f func(resolv.EntityRef) error) error {
	node := s.startFrom
	fmt.Println("Walking")

	// If we're somewhere underneath an OCFL object, we need to find the path of
	// the object root in order to get its manifest and walk it.
	if node.Type < ocfl.Object {
		var err error
		node, err = findRoot(node, ocfl.Object)
		if err != nil {
			return err
		}
	}

	if node.Type == ocfl.Root && s.contains(*node) {
		err := f(*node)
		if err != nil {
			return err
		}
	}

	startPath := node.Addr
	if startPath == "" {
		startPath = s.root.Addr
	}

	// At this point, node points to an ocfl root, intermediate node, or an ocfl object root
	err := fsWalk(startPath, func(ospath string, e *godirwalk.Dirent) (bool, error) {

		// We dont' care about regular files
		if !e.IsDir() && !e.IsSymlink() {
			return dontGoDeeper, nil
		}

		// An object?  If so, walk its manifest instead of the files under it
		if objectRoot, _, err := isRoot(ospath, ocfl.Object); objectRoot && err == nil {

			return dontGoDeeper, s.walkObject(ospath, f)
		} else if err != nil {
			return dontGoDeeper, err
		}

		// Skip root, process intermdiate and continue
		if ospath != s.root.Addr && s.contains(resolv.EntityRef{Type: ocfl.Intermediate}) {
			err := f(resolv.EntityRef{
				ID:     strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(ospath, s.root.Addr)), "/"),
				Addr:   ospath,
				Type:   ocfl.Intermediate,
				Parent: s.root,
			})
			if err != nil {
				return dontGoDeeper, err
			}
		}

		return goDeeper, nil
	})
	if err != nil {
		return errors.Wrapf(err, "error performing walk")
	}
	return nil
}

// Walk the OCFL manifest
func (s *scope) walkObject(path string, f func(resolv.EntityRef) error) (err error) {

	inv, err := readMetadata(path)
	if err != nil {
		return err
	}

	object := resolv.EntityRef{
		ID:     inv.ID,
		Type:   ocfl.Object,
		Parent: s.root,
		Addr:   path,
	}

	if s.contains(object) {
		err := f(object)
		if err != nil {
			return err
		}
	}

	if s.desired.Type <= ocfl.Version {
		return s.walkVersions(inv, &object, f)
	}

	return nil
}

// Walk the versions in an OCFL manifest
func (s *scope) walkVersions(inv *metadata.Inventory, object *resolv.EntityRef, f func(resolv.EntityRef) error) error {
	versions := inv.Versions

	// A little awkward, but if we want a specific version or file instead of all versions or files...
	if s.startFrom.Type == ocfl.Version || s.startFrom.Type == ocfl.File {

		scopeVersion, _ := findRoot(s.startFrom, ocfl.Version) // An error here is impossible

		if _, ok := versions[scopeVersion.ID]; !ok {
			return fmt.Errorf("No version %s exists in %s", scopeVersion.ID, object.ID)
		}

		versions = map[string]metadata.Version{
			scopeVersion.ID: {},
		}
	}

	for vID := range versions {
		fmt.Printf("StartFrom %s, %s, %s\n", s.startFrom.Coords(), s.startFrom.Type, s.startFrom.Addr)
		version := resolv.EntityRef{
			ID:     vID,
			Type:   ocfl.Version,
			Parent: object,
			Addr:   filepath.Join(object.Addr, vID),
		}

		if s.contains(version) {
			err := f(version)
			if err != nil {
				return err
			}
		}

		if s.desired.Type <= ocfl.File {
			files, _ := inv.Files(vID)
			for _, file := range files {

				fileRef := resolv.EntityRef{
					ID:     file.LogicalPath,
					Type:   ocfl.File,
					Parent: &version,
					Addr:   filepath.Join(object.Addr, file.PhysicalPath),
				}

				if !s.contains(fileRef) {
					continue
				}

				err := f(fileRef)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (s scope) contains(r resolv.EntityRef) bool {
	if s.desired.Type == ocfl.Any {
		return true
	}

	// If we're under the starting point(actually walking),
	// just compare to desired type
	if r.Type != s.startFrom.Type {
		return s.desired.Type == r.Type
	}

	// Otherwise, if our starting point _is_ the desired type,
	// then we're not really walking, we're listing that one entity
	// So compare the desired with the encountered for coordinate equality
	for a, b := s.desired, &r; a.Parent != nil && b.Parent != nil; a, b = a.Parent, b.Parent {
		if a.ID != b.ID {
			return false
		}
	}

	return r.Type <= s.desired.Type
}

type skip struct {
	action godirwalk.ErrorAction
}

func (skip) Error() string {
	return "node is skipped"
}

// Callback to be invoked each time a fs entry is encountered.
// Returns a boolean indicating whether the current fs entry should be a
// considered a terminal (leaf) node.  If true, any children will not be
// walked.  Any error will terminate a walk entirely.
type fsCallback func(ospath string, e *godirwalk.Dirent) (terminal bool, err error)

func fsWalk(dir string, f fsCallback) error {

	if _, err := os.Stat(dir); err != nil {
		return errors.Wrapf(err, "error walking directory %s", dir)
	}

	return godirwalk.Walk(dir, &godirwalk.Options{
		Callback: func(ospath string, dirent *godirwalk.Dirent) error {
			terminal, err := f(ospath, dirent)
			if err != nil {
				return errors.Wrap(err, "terminating walk due to error")
			}
			if terminal {
				return skip{godirwalk.SkipNode}
			}
			return nil
		},
		ErrorCallback: func(ospath string, err error) godirwalk.ErrorAction {
			s, skip := errors.Cause(err).(skip)
			if skip {
				return s.action
			}

			return godirwalk.Halt
		},
		Unsorted:            true,
		FollowSymbolicLinks: true,
	},
	)
}
