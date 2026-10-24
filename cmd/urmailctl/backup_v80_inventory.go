package main

import (
	"archive/tar"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

func collectV80Inventory(base string) (v80Inventory, error) {
	inventory := v80Inventory{Members: []v80Member{}, Excluded: []v80Exclusion{}, Blockers: []string{}}
	source, err := os.OpenRoot(base)
	if err != nil {
		return inventory, errors.New("profile source tree cannot be opened")
	}
	defer func() { _ = source.Close() }()
	for _, spec := range v80Roots {
		info, statErr := source.Lstat(spec.Path)
		if os.IsNotExist(statErr) && !spec.Required {
			continue
		}
		if statErr != nil {
			inventory.Blockers = append(inventory.Blockers, "SOURCE_ROOT_MISSING_OR_UNREADABLE")
			continue
		}
		if spec.Directory && !info.IsDir() {
			inventory.Blockers = append(inventory.Blockers, "SOURCE_ROOT_TYPE_INVALID")
			continue
		}
		rootPath, relative := spec.Path, "."
		if !spec.Directory {
			rootPath = path.Dir(spec.Path)
			relative = path.Base(spec.Path)
		}
		root, openErr := source.OpenRoot(rootPath)
		if openErr != nil {
			inventory.Blockers = append(inventory.Blockers, "SOURCE_ROOT_CAPABILITY_INVALID")
			continue
		}
		rootInfo, rootErr := root.Stat(".")
		if rootErr != nil {
			_ = root.Close()
			return inventory, errors.New("source root identity cannot be checked")
		}
		add := func(name string, info fs.FileInfo) error {
			logical := spec.Path
			if name != "." && spec.Directory {
				logical = path.Join(spec.Path, name)
			}
			if !v80Allowed(logical) {
				return errors.New("source member is outside the exact profile")
			}
			target := ""
			if info.Mode()&os.ModeSymlink != 0 {
				var linkErr error
				target, linkErr = root.Readlink(name)
				if linkErr != nil {
					return errors.New("source symlink cannot be read")
				}
				if v80RuntimeLink(logical, target) {
					inventory.Excluded = append(inventory.Excluded, v80Exclusion{logical, target, "nonpersistent_roundcube_runtime"})
					return nil
				}
			}
			header, headerErr := tar.FileInfoHeader(info, target)
			if headerErr != nil {
				return errors.New("source member type cannot be archived")
			}
			kind := ""
			switch {
			case info.Mode().IsRegular():
				kind = "file"
			case info.IsDir():
				kind = "directory"
			case info.Mode()&os.ModeSymlink != 0:
				kind = "symlink"
			default:
				return errors.New("source contains an unsupported special object")
			}
			size := header.Size
			if kind != "file" {
				size = 0
			}
			if size < 0 || size > v80MaxBytes-inventory.Bytes {
				return errors.New("profile byte limit exceeded")
			}
			if kind == "file" {
				inventory.Bytes += size
			}
			inventory.Members = append(inventory.Members, v80Member{Name: logical, Kind: kind, Size: size, Mode: header.Mode, UID: header.Uid, GID: header.Gid, ModifiedAt: info.ModTime().UTC(), LinkTarget: target, info: info, rootPath: rootPath, relative: name, rootInfo: rootInfo})
			if len(inventory.Members) > v80MaxMembers {
				return errors.New("profile member limit exceeded")
			}
			return nil
		}
		if spec.Directory {
			err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return errors.New("source tree changed or cannot be read")
				}
				memberInfo, infoErr := entry.Info()
				if infoErr != nil {
					return errors.New("source member cannot be inspected")
				}
				return add(name, memberInfo)
			})
		} else {
			err = add(relative, info)
		}
		_ = root.Close()
		if err != nil {
			inventory.Blockers = append(inventory.Blockers, "SOURCE_TREE_UNSUPPORTED_OR_CHANGED")
		}
	}
	if err = validateV80Layout(inventory.Members); err != nil {
		inventory.Blockers = append(inventory.Blockers, "PRESERVED_LAYOUT_INCOMPLETE_OR_UNSAFE")
	}
	if len(inventory.Blockers) == 0 {
		configs, fpm, configErr := readV80ConfigMaterial(base, inventory.Members)
		if configErr != nil || validateV80ConfigMaterial(configs, fpm) != nil {
			inventory.Blockers = append(inventory.Blockers, "SOURCE_CONFIGURATION_REQUIRES_PROFILE_REVIEW")
		}
	}
	v80SortInventory(&inventory)
	return inventory, nil
}

func openV80Member(base string, member v80Member) (*os.File, error) {
	source, err := os.OpenRoot(base)
	if err != nil {
		return nil, errors.New("profile source root changed")
	}
	defer func() { _ = source.Close() }()
	root, err := source.OpenRoot(member.rootPath)
	if err != nil {
		return nil, errors.New("profile source capability changed")
	}
	defer func() { _ = root.Close() }()
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(member.rootInfo, rootInfo) {
		return nil, errors.New("profile source root identity changed")
	}
	file, err := root.OpenFile(member.relative, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("profile source member changed")
	}
	info, err := file.Stat()
	if err != nil || !v80SameInfo(member.info, info) {
		_ = file.Close()
		return nil, errors.New("profile source member identity changed")
	}
	return file, nil
}
func readV80ConfigMaterial(base string, members []v80Member) (map[string][]byte, bool, error) {
	configs := map[string][]byte{}
	fpm := false
	for _, member := range members {
		if member.Kind != "file" || !v80ConfigurationMember(member.Name) {
			continue
		}
		file, err := openV80Member(base, member)
		if err != nil {
			return nil, false, err
		}
		limit := int64(1 << 20)
		if member.Name == "home/user-data/mail/users.sqlite" || member.Name == "home/user-data/mail/roundcube/roundcube.sqlite" || member.Name == "home/user-data/owncloud/owncloud.db" {
			limit = 16
		} else if member.Size > limit {
			_ = file.Close()
			return nil, false, errors.New("profile configuration exceeds its bound")
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, limit))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, false, errors.New("profile configuration cannot be inspected")
		}
		configs[member.Name] = raw
		if strings.HasPrefix(member.Name, "etc/php/") && v80FPMPattern.Match(raw) {
			fpm = true
		}
	}
	return configs, fpm, nil
}

func verifyV80InventoryStable(base string, members []v80Member) error {
	source, err := os.OpenRoot(base)
	if err != nil {
		return errors.New("profile source root changed")
	}
	defer func() { _ = source.Close() }()
	for _, member := range members {
		root, openErr := source.OpenRoot(member.rootPath)
		if openErr != nil {
			return errors.New("profile source root changed")
		}
		rootInfo, rootErr := root.Stat(".")
		info, statErr := root.Lstat(member.relative)
		if rootErr != nil || statErr != nil || !os.SameFile(member.rootInfo, rootInfo) || !v80SameInfo(member.info, info) {
			_ = root.Close()
			return errors.New("source changed while the profile bundle was created")
		}
		if member.Kind == "symlink" {
			target, linkErr := root.Readlink(member.relative)
			if linkErr != nil || target != member.LinkTarget {
				_ = root.Close()
				return errors.New("source symlink changed")
			}
		}
		_ = root.Close()
	}
	return nil
}
