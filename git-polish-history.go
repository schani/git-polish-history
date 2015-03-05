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

const toolName = "polish-history"

type state struct {
	repo           *git.Repository
	branchName     string
	buildCommand   string
	buildDirectory string
	commits        []*git.Commit
}

const (
	buildCommandFilename   = "build-command"
	buildDirectoryFilename = "build-directory"
	commitsFilename        = "commits"
	branchFilename         = "branch"
)

func stateDir(repo *git.Repository) string {
	return path.Join(repo.Path(), toolName)
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

func getCommitFromName(repo *git.Repository, name string) (*git.Commit, error) {
	obj, err := repo.RevparseSingle(name)
	if err != nil {
		return nil, err
	}

	commit, err := repo.LookupCommit(obj.Id())
	if err != nil {
		return nil, err
	}

	return commit, nil
}

func branchName(repo *git.Repository) (string, error) {
	ref, err := repo.Head()
	if err != nil {
		return "", err
	}

	if !ref.IsBranch() {
		return "", nil
	}

	for ref.Type() == git.ReferenceSymbolic {
		ref, err = repo.LookupReference(ref.SymbolicTarget())
		if err != nil {
			return "", err
		}
	}

	if ref.Type() != git.ReferenceOid {
		return "", errors.New("Unknown reference type")
	}

	fmt.Printf("branch is %s\n", ref.Name())

	return ref.Name(), nil
}

func readStateFile(repo *git.Repository, name string) (string, error) {
	bytes, err := ioutil.ReadFile(stateFile(repo, name))
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func writeStateFile(repo *git.Repository, name string, contents string) error {
	return ioutil.WriteFile(stateFile(repo, name), []byte(contents), 0644)

}

func readState(repo *git.Repository) (state, error) {
	buildCommand, err := readStateFile(repo, buildCommandFilename)
	if err != nil {
		return state{}, err
	}

	buildDirectory, err := readStateFile(repo, buildDirectoryFilename)
	if err != nil {
		return state{}, err
	}

	commitsString, err := readStateFile(repo, commitsFilename)
	if err != nil {
		return state{}, err
	}

	branchName, err := readStateFile(repo, branchFilename)
	if err != nil {
		return state{}, err
	}

	commitIds := strings.Split(commitsString, "\n")
	commits := []*git.Commit{}
	for _, commitId := range commitIds {
		commit, err := getCommitFromName(repo, commitId)
		if err != nil {
			return state{}, err
		}

		commits = append(commits, commit)
	}

	st := state{
		repo:           repo,
		branchName:     branchName,
		buildCommand:   buildCommand,
		buildDirectory: buildDirectory,
		commits:        commits,
	}

	return st, nil
}

func writeState(st state) error {
	err := os.Mkdir(stateDir(st.repo), 0755)
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}

	err = writeStateFile(st.repo, buildCommandFilename, st.buildCommand)
	if err != nil {
		return err
	}

	err = writeStateFile(st.repo, buildDirectoryFilename, st.buildDirectory)
	if err != nil {
		return err
	}

	err = writeStateFile(st.repo, branchFilename, st.branchName)
	if err != nil {
		return err
	}

	commitIds := []string{}
	for _, commit := range st.commits {
		commitIds = append(commitIds, commit.Id().String())
	}

	err = writeStateFile(st.repo, commitsFilename, strings.Join(commitIds, "\n"))
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

func setHead(st state, commit *git.Commit, how string) error {
	sig, err := st.repo.DefaultSignature()
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("%s (%s): %s", toolName, how, commit.Summary())

	if st.branchName == "" {
		return st.repo.SetHeadDetached(commit.Id(), sig, msg)
	} else {
		currentBranch, err := branchName(st.repo)
		if err != nil {
			return err
		}

		if st.branchName != currentBranch {
			fmt.Fprintf(os.Stderr, "We're not on the original branch `%s` anymore.\n", st.branchName)
			os.Exit(1)
		}

		ref, err := st.repo.LookupReference(st.branchName)
		if err != nil {
			return err
		}

		ref, err = ref.SetTarget(commit.Id(), sig, msg)
		if err != nil {
			return err
		}

		return nil
	}

}

func checkout(st state, commit *git.Commit) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}

	err = st.repo.CheckoutTree(tree, &git.CheckoutOpts{Strategy: git.CheckoutSafe})
	if err != nil {
		return err
	}

	err = setHead(st, commit, "checkout")
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
	var innerErr error
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
	cmd.Dir = st.buildDirectory
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
			return fmt.Errorf("Commit %s should have exactly one parent.", commit.Id())
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

			err = checkout(st, commit)
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

			err = setHead(st, newCommit, "cherry-pick")
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

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	st, err := readState(repo)
	if doContinue {
		if err != nil {
			return fmt.Errorf("Could not read state: %v\n", err)
		}

		if c.GlobalIsSet("build") {
			st.buildCommand = c.GlobalString("build")
			st.buildDirectory = cwd
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
		if err == nil {
			fmt.Fprintf(os.Stderr, "Error: State is present.\n")
			os.Exit(1)
		}

		branch, err := branchName(repo)
		if err != nil {
			return err
		}

		startCommitName := c.Args()[0]

		commits, err := getCommits(repo, startCommitName)
		if err != nil {
			return err
		}

		startCommit, err := getCommitFromName(repo, startCommitName)
		if err != nil {
			return err
		}

		st = state{
			repo:           repo,
			branchName:     branch,
			buildCommand:   c.String("build"),
			buildDirectory: cwd,
			commits:        commits,
		}

		err = checkout(st, startCommit)
		if err != nil {
			return err
		}
	}

	err = work(st)
	if err != nil {
		return err
	}

	return nil
}

func appAction(c *cli.Context) error {
	return appActualAction(c, false)
}

func continueAction(c *cli.Context) error {
	return appActualAction(c, true)
}

// FIXME: move back to original commit
func abortAction(c *cli.Context) error {
	repo, err := openRepo()
	if err != nil {
		return err
	}

	return deleteState(repo)
}

func actionRunner(action func(*cli.Context) error) func(*cli.Context) {
	return func(c *cli.Context) {
		err := action(c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
}

func main() {
	app := cli.NewApp()
	app.Name = fmt.Sprintf("git-%s", toolName)
	app.Usage = "Reform history by fixing the build on each commit"
	app.Author = "Mark Probst"
	app.Email = "mark.probst@gmail.com"
	app.Commands = []cli.Command{
		{
			Name:   "continue",
			Usage:  "Continue current run",
			Action: actionRunner(continueAction),
		},
		{
			Name:   "abort",
			Usage:  "Abort current run",
			Action: actionRunner(abortAction),
		},
	}
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "build",
			Value: "make -j4",
			Usage: "Build command",
		},
	}
	app.Action = actionRunner(appAction)

	app.Run(os.Args)
}
