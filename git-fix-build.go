package main

import (
	"errors"
	"fmt"
	"github.com/codegangsta/cli"
	"github.com/libgit2/git2go"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
)

type state struct {
	repo         *git.Repository
	buildCommand string
	commits      []*git.Commit
}

const (
	buildCommandFilename = "build-command"
	commitsFilename      = "commits"
)

func stateDir(repo *git.Repository) string {
	return path.Join(repo.Path(), "fix-build")
}

func stateFile(repo *git.Repository, name string) string {
	return path.Join(stateDir(repo), name)
}

func openRepo() (*git.Repository, error) {
	repoPath, err := git.Discover(".", false, []string{"/"})
	if err != nil {
		return nil, err
	}

	repo, err := git.OpenRepository(repoPath)
	if err != nil {
		return nil, err
	}

	return repo, err
}

func readState(repo *git.Repository) (state, error) {
	buildCommandBytes, err := ioutil.ReadFile(stateFile(repo, buildCommandFilename))
	if err != nil {
		return state{}, err
	}

	commitsBytes, err := ioutil.ReadFile(stateFile(repo, commitsFilename))
	if err != nil {
		return state{}, err
	}

	commitIds := strings.Split(string(commitsBytes), "\n")
	commits := []*git.Commit{}
	for _, commitId := range commitIds {
		obj, err := repo.RevparseSingle(commitId)
		if err != nil {
			return state{}, err
		}

		commit, err := repo.LookupCommit(obj.Id())
		if err != nil {
			return state{}, err
		}

		commits = append(commits, commit)
	}

	return state{repo: repo, buildCommand: string(buildCommandBytes), commits: commits}, nil
}

func writeState(st state) error {
	err := os.Mkdir(stateDir(st.repo), 0755)
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}

	err = ioutil.WriteFile(stateFile(st.repo, buildCommandFilename), []byte(st.buildCommand), 0644)
	if err != nil {
		return err
	}

	commitIds := []string{}
	for _, commit := range st.commits {
		commitIds = append(commitIds, commit.Id().String())
	}

	err = ioutil.WriteFile(stateFile(st.repo, commitsFilename), []byte(strings.Join(commitIds, "\n")), 0644)
	if err != nil {
		return err
	}

	return nil
}

func deleteState(repo *git.Repository) error {
	return os.RemoveAll(stateDir(repo))
}

func hasChanges(repo *git.Repository) (bool, error) {
	if repo.State() != git.RepositoryStateNone {
		return true, nil
	}

	obj, err := repo.RevparseSingle("HEAD^{tree}")
	if err != nil {
		return true, err
	}

	tree, err := repo.LookupTree(obj.Id())
	if err != nil {
		return true, err
	}

	diff, err := repo.DiffTreeToWorkdir(tree, nil)
	if err != nil {
		return true, err
	}

	numDeltas, err := diff.NumDeltas()
	if err != nil {
		return true, err
	}

	return numDeltas != 0, nil
}

func setHead(repo *git.Repository, commit *git.Commit) error {
	// FIXME: append commit description to log message
	// FIXME: use proper signature
	return repo.SetHeadDetached(commit.Id(), commit.Author(), "fix-build")
}

func checkout(repo *git.Repository, commit *git.Commit) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}

	err = repo.CheckoutTree(tree, &git.CheckoutOpts{Strategy: git.CheckoutSafe})
	if err != nil {
		return err
	}

	err = setHead(repo, commit)
	if err != nil {
		return err
	}

	return nil
}

func getCommits(repo *git.Repository, startName string) ([]*git.Commit, error) {
	startObj, err := repo.RevparseSingle(startName)
	if err != nil {
		return nil, err
	}

	startObjHash := startObj.Id().String()
	fmt.Printf("start is %s\n", startObjHash)

	walk, err := repo.Walk()
	if err != nil {
		return nil, err
	}

	err = walk.PushHead()
	if err != nil {
		return nil, err
	}
	walk.Sorting(git.SortTopological)

	commits := []*git.Commit{}
	var innerErr error = nil
	err = walk.Iterate(func(commit *git.Commit) bool {
		if commit.Id().String() == startObjHash {
			return false
		}
		if commit.ParentCount() != 1 {
			innerErr = errors.New("Reached a commit with more than one parents")
			return false
		}
		commits = append(commits, commit)
		fmt.Printf("Commit %s\n", commit.Id())
		return true
	})
	if innerErr != nil {
		return nil, innerErr
	}
	if err != nil {
		return nil, err
	}

	return commits, nil
}

func tryBuild(st state) (bool, error) {
	// FIXME: build directory
	cmd := exec.Command("/bin/sh", "-c", st.buildCommand)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		switch err.(type) {
		case *exec.ExitError:
			return false, nil
		default:
			return false, err
		}
	}
	return true, nil
}

func work(st state) error {
	for len(st.commits) > 0 {
		// Get and remove last commit
		commit := st.commits[len(st.commits)-1]
		st.commits = st.commits[:len(st.commits)-1]

		if commit.ParentCount() != 1 {
			return errors.New(fmt.Sprintf("Commit %s should have exactly one parent.", commit.Id()))
		}
		parent := commit.Parent(0)

		headCommitObj, err := st.repo.RevparseSingle("HEAD")
		if err != nil {
			return err
		}

		// If HEAD is the same as the next commit's parent, we
		// can just checkout out that commit.  Otherwise we
		// have to cherry-pick it.
		if headCommitObj.Id().String() == parent.Id().String() {
			fmt.Printf("*** checking out %s\n", commit.Id())

			err = checkout(st.repo, commit)
			if err != nil {
				return err
			}
		} else {
			fmt.Printf("*** cherry-picking %s\n", commit.Id())

			opts, err := git.DefaultCherrypickOptions()
			if err != nil {
				return err
			}

			err = st.repo.Cherrypick(commit, opts)
			if err != nil {
				return err
			}

			index, err := st.repo.Index()
			if err != nil {
				return err
			}

			if index.HasConflicts() {
				fmt.Fprintf(os.Stderr, "Cherry-pick conflicts.\n")
				err = writeState(st)
				if err != nil {
					return err
				}
				return nil
			}

			treeId, err := index.WriteTree()
			if err != nil {
				return err
			}

			tree, err := st.repo.LookupTree(treeId)
			if err != nil {
				return err
			}

			headCommit, err := st.repo.LookupCommit(headCommitObj.Id())
			if err != nil {
				return err
			}

			newCommitId, err := st.repo.CreateCommit("", commit.Author(), commit.Committer(), commit.Message(), tree, headCommit)
			if err != nil {
				return err
			}

			newCommit, err := st.repo.LookupCommit(newCommitId)
			if err != nil {
				return err
			}

			err = setHead(st.repo, newCommit)
			if err != nil {
				return err
			}

			err = st.repo.StateCleanup()
			if err != nil {
				return err
			}
		}

		builds, err := tryBuild(st)
		if err != nil {
			return err
		}

		if !builds {
			fmt.Fprintf(os.Stderr, "Build failed.\n")
			err = writeState(st)
			if err != nil {
				return err
			}
			return nil
		}
	}

	fmt.Printf("done")

	err := deleteState(st.repo)
	if err != nil {
		return err
	}

	return nil
}

func appActualAction(c *cli.Context, doContinue bool) error {
	if doContinue {
		if len(c.Args()) != 0 {
			cli.ShowAppHelp(c)
			os.Exit(1)
		}
	} else {
		if len(c.Args()) != 1 {
			cli.ShowAppHelp(c)
			os.Exit(1)
		}
	}

	repo, err := openRepo()
	if err != nil {
		return err
	}

	changes, err := hasChanges(repo)
	if err != nil {
		return err
	}
	if changes {
		fmt.Fprintf(os.Stderr, "Error: Working directory or index has changes\n")
		os.Exit(1)
	}

	st, err := readState(repo)
	if doContinue {
		if err != nil {
			return errors.New(fmt.Sprintf("Could not read state: %v\n", err))
		}

		if c.IsSet("build") {
			st.buildCommand = c.String("build")
		}

		builds, err := tryBuild(st)
		if err != nil {
			return err
		}

		if !builds {
			fmt.Fprintf(os.Stderr, "Build failed.\n")
			os.Exit(1)
		}
	} else {
		startCommit := c.Args()[0]

		if err == nil {
			fmt.Fprintf(os.Stderr, "Error: State is present.\n")
			os.Exit(1)
		}

		commits, err := getCommits(repo, startCommit)
		if err != nil {
			return err
		}

		st = state{repo: repo, buildCommand: c.String("build"), commits: commits}
	}

	err = work(st)
	if err != nil {
		return err
	}

	return nil
}

func appAction(c *cli.Context) {
	err := appActualAction(c, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func continueAction(c *cli.Context) {
	err := appActualAction(c, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	app := cli.NewApp()
	app.Name = "git-fix-build"
	app.Usage = "fix builds by amending"
	app.Author = "Mark Probst"
	app.Email = "mark.probst@gmail.com"
	app.Commands = []cli.Command{
		{
			Name:   "continue",
			Usage:  "Continue current run",
			Action: continueAction,
		},
	}
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "build",
			Value: "make -j4",
			Usage: "Build command",
		},
	}
	app.Action = appAction

	app.Run(os.Args)
}
