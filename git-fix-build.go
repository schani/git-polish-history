package main

import (
	"fmt"
	"errors"
	"os"
	"os/exec"
	"github.com/libgit2/git2go"
)

func hasChanges (repo *git.Repository) (bool, error) {
	obj, err := repo.RevparseSingle ("HEAD^{tree}")
	if err != nil {
		return true, err
	}

	tree, err := repo.LookupTree (obj.Id())
	if err != nil {
		return true, err
	}

	diff, err := repo.DiffTreeToWorkdir (tree, nil)
	if err != nil {
		return true, err
	}

	numDeltas, err := diff.NumDeltas ()
	if err != nil {
		return true, err
	}

	return numDeltas != 0, nil
}

func checkout (repo *git.Repository, commit *git.Commit) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}

	err = repo.CheckoutTree (tree, &git.CheckoutOpts { Strategy: git.CheckoutSafe })
	if err != nil {
		return err
	}

	// FIXME: append commit description to log message
	// FIXME: use proper signature
	err = repo.SetHeadDetached (commit.Id(), commit.Author(), "fix-build")
	if err != nil {
		return err
	}

	return nil
}

func work (startName string, buildCommand string) error {
	repoPath, err := git.Discover (".", false, []string { "/" })
	if err != nil {
		return err
	}

	repo, err := git.OpenRepository (repoPath)
	if err != nil {
		return err
	}

	changes, err := hasChanges (repo)
	if err != nil {
		return err
	}
	if changes {
		return errors.New("Working directory has changes")
	}

	startObj, err := repo.RevparseSingle (startName)
	if err != nil {
		return err
	}

	startObjHash := startObj.Id().String()
	fmt.Printf ("start is %s\n", startObjHash)

	walk, err := repo.Walk ()
	if err != nil {
		return err
	}

	err = walk.PushHead ()
	if err != nil {
		return err
	}
	walk.Sorting (git.SortTopological)

	commits := []*git.Commit {}
	var innerErr error = nil
	err = walk.Iterate (func (commit *git.Commit) bool {
		if commit.Id().String() == startObjHash {
			fmt.Printf ("Found start commit")
			return false
		}
		if commit.ParentCount () != 1 {
			innerErr = errors.New ("Reached a commit with more than one parents")
			return false
		}
		commits = append (commits, commit)
		fmt.Printf ("Commit %s\n", commit.Id())
		return true
	})
	if innerErr != nil {
		return innerErr
	}
	if err != nil {
		return err
	}

	for i := len (commits) - 1; i >= 0; i-- {
		commit := commits [i]
		fmt.Printf ("*** %s\n", commit.Id())

		err = checkout (repo, commit)
		if err != nil {
			return err
		}

		cmd := exec.Command ("/bin/sh", "-c", buildCommand)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run ()
		if err != nil {
			switch err.(type) {
			case *exec.ExitError:
				fmt.Printf ("build failed")
				os.Exit (0)
			default:
				return err
			}
		}
	}

	fmt.Printf ("Iterated over %d commits\n", len (commits))
	return nil
}

func main () {
	err := work ("d5fcd29868a9fcdcda3b1be4af7f8296eec7e234", "make -j4")
	if err != nil {
		fmt.Printf ("Error: %v\n", err)
		os.Exit (1)
	}
}
