package resolv

import (
	"fmt"
	"io"

	"github.com/birkland/ocfl"
)

// EntityRef represents a single OCFL entity.
type EntityRef struct {
	ID     string     // The logical ID of the entity (string, uri, or relative file path)
	Addr   string     // Physical address of the entity (absolute file path or URI)
	Parent *EntityRef // Parent of next highest type that isn't an intermediate node (e.g. object parent is root)
	Type   ocfl.Type  // Type of entity
}

// Coords returns a slice of the logical coordinates of an entity ref, of
// the form {objectID, versionID, logicalFilePath}
func (e EntityRef) Coords() []string {
	var coords []string
	for ref := &e; ref != nil && ref.Type != ocfl.Root; ref = ref.Parent {
		coords = append([]string{ref.ID}, coords...)
	}

	return coords
}

// Options for establishing a read/write session on an OCFL object.
type Options struct {
	Create           bool     // If true, this will create a new object if one does not exist.
	DigestAlgorithms []string // Desired fixity digest algorithms when writing new files.
	User             struct {
		Name    string
		Address string
	}
}

// Session allows reading or writing to the an OCFL object. Each session is bound to a single
// OCFL object version - either a pre-existing version, or an uncommitted new version.
type Session interface {
	Put(lpath string, r io.Reader) error // Put file content at the given logical path
	// TODO: Delete(lpath string) error
	// TODO: Move(src, dest string) error
	// TODO: Read(lpath string) (io.Reader, error)
	// TODO: Commit() error
	// TODO: Close() error
}

// Opener opens an OCFL object session, potentially allowing reading and writing to it.
type Opener interface {
	Open(id string, opts Options) Session // Open an OCFL object
}

// Walker crawls through a bounded scope of OCFL entities "underneath" a start
// location.  Given a location and a desired type, Walker will invoke the provided
// callback any time an entity of the desired type is encountered.
//
// The walk locaiton may either be a single physical address (such as a file path or URI),
// or it may be a sequence of logical OCFL identifiers, such as {objectID, versionID, logicalFilePath}
// When providing logical identifiers, object IDs may be provided on their own, version IDs must be preceded
// by an object ID, and logical file paths must be preceded by the version ID.
//
// If no location is given, the scope of the walk is implied to be the entirety of content under an OCFL root.
type Walker interface {
	Walk(desired Select, cb func(EntityRef) error, loc ...string) error
}

// Select indicates desired properties of matching OCFL entities
type Select struct {
	Type ocfl.Type // Desired OCFL type
	Head bool      // True if desired files or versions must be in the head revision
}

// Driver provides basic OCFL access via some backend
type Driver interface {
	Walker
	Opener
}

type Config struct {
	Root    string
	Drivers []Driver
}

// Cxt establishes a context for resolving OCFL entities,
// e.g. an OCFL root, or a user
type Cxt struct {
	root   *EntityRef
	config Config
}

// NewCxt establishes a new resolver context
func Init(cfg Config) (*Cxt, error) {
	cxt := &Cxt{
		config: cfg,
	}
	if cfg.Root != "" {
		for _, d := range cfg.Drivers {
			err := d.Walk(Select{Type: ocfl.Root}, func(r EntityRef) error {
				cxt.root = &r
				return nil
			}, cfg.Root)
			if err != nil {
				continue
			}
			return cxt, nil
		}
	}
	return nil, fmt.Errorf("No suitable driver found")
}
