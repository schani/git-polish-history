package main

import (
	"errors"
	"fmt"
	"github.com/codegangsta/cli"
	"github.com/schani/git2go"
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

func gitFile(repo *git.Repository, name string) string {
	return path.Join(repo.Path(), name)
}

func stateDir(repo *git.Repository) string {
	return gitFile(repo, toolName)
}

func stateFile(repo *git.Repository, name string) string {
	return path.Join(stateDir(repo), name)
}

func workdirFile(repo *git.Repository, name string) string {
	return path.Join(repo.Workdir(), name)
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

func dereferenceHead(repo *git.Repository) (*git.Reference, error) {
	ref, err := repo.Head()
	if err != nil {
		return nil, err
	}

	if !ref.IsBranch() {
		return ref, nil
	}

	for ref.Type() == git.ReferenceSymbolic {
		ref, err = repo.LookupReference(ref.SymbolicTarget())
		if err != nil {
			return nil, err
		}
	}

	if ref.Type() != git.ReferenceOid {
		return nil, errors.New("Unknown reference type")
	}

	return ref, nil
}

func branchName(repo *git.Repository) (string, error) {
	ref, err := dereferenceHead(repo)
	if err != nil {
		return "", err
	}

	if !ref.IsBranch() {
		return "", nil
	}

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

func readCommitsFromFile(repo *git.Repository, path string) ([]*git.Commit, error) {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	str := strings.TrimSpace(string(bytes))

	commitIds := strings.Fields(str)
	commits := []*git.Commit{}
	for _, commitId := range commitIds {
		commit, err := getCommitFromName(repo, commitId)
		if err != nil {
			return nil, err
		}

		commits = append(commits, commit)
	}

	return commits, nil
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

	branchName, err := readStateFile(repo, branchFilename)
	if err != nil {
		return state{}, err
	}

	commits, err := readCommitsFromFile(repo, stateFile(repo, commitsFilename))
	if err != nil {
		return state{}, err
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

func filesFromDiff(diff *git.Diff, fileSet map[string]bool) error {
	numDeltas, err := diff.NumDeltas()
	if err != nil {
		return err
	}

	for i := 0; i < numDeltas; i++ {
		delta, err := diff.GetDelta(i)
		if err != nil {
			return err
		}

		fileSet[delta.OldFile.Path] = true
		fileSet[delta.NewFile.Path] = true
	}

	return nil
}

func changedFiles(repo *git.Repository) ([]string, error) {
	obj, err := repo.RevparseSingle("HEAD^{tree}")
	if err != nil {
		return nil, err
	}

	tree, err := repo.LookupTree(obj.Id())
	if err != nil {
		return nil, err
	}

	/*
		opts, err := git.DefaultDiffOptions()
		if err != nil {
			return nil, err
		}
	*/

	fileSet := map[string]bool{}

	diff, err := repo.DiffTreeToWorkdirWithIndex(tree, nil)
	if err != nil {
		return nil, err
	}
	err = filesFromDiff(diff, fileSet)
	if err != nil {
		return nil, err
	}

	diff, err = repo.DiffTreeToWorkdir(tree, nil)
	if err != nil {
		return nil, err
	}
	err = filesFromDiff(diff, fileSet)
	if err != nil {
		return nil, err
	}

	diff, err = repo.DiffIndexToWorkdir(nil, nil)
	if err != nil {
		return nil, err
	}
	err = filesFromDiff(diff, fileSet)
	if err != nil {
		return nil, err
	}

	files := []string{}
	for file, _ := range fileSet {
		files = append(files, file)
	}

	return files, nil
}

// FIXME: use status to speed this up?
func hasChanges(repo *git.Repository) (bool, error) {
	files, err := changedFiles(repo)
	if err != nil {
		return true, err
	}

	if len(files) > 0 {
		fmt.Fprintf(os.Stderr, "Changes in working directory and/or index:\n\n")
		for _, file := range files {
			fmt.Fprintf(os.Stderr, "\t%s\n", file)
		}
	}

	return len(files) > 0, nil
}

func makeCommit(st state, commit *git.Commit, cleanupConflicts bool) (*git.Commit, error) {
	index, err := st.repo.Index()
	if err != nil {
		return nil, err
	}

	if cleanupConflicts {
		index.CleanupConflicts()
	}

	treeId, err := index.WriteTree()
	if err != nil {
		return nil, err
	}

	tree, err := st.repo.LookupTree(treeId)
	if err != nil {
		return nil, err
	}

	committer, err := st.repo.DefaultSignature()
	if err != nil {
		return nil, err
	}

	headCommitObj, err := st.repo.RevparseSingle("HEAD")
	if err != nil {
		return nil, err
	}

	headCommit, err := st.repo.LookupCommit(headCommitObj.Id())
	if err != nil {
		return nil, err
	}

	var newCommitId *git.Oid

	if commit != nil {
		newCommitId, err = st.repo.CreateCommit("", commit.Author(), committer, commit.Message(), tree, headCommit)
	} else {
		newCommitId, err = headCommit.Amend("", headCommit.Author(), committer, headCommit.Message(), tree)
	}
	if err != nil {
		return nil, err
	}

	newCommit, err := st.repo.LookupCommit(newCommitId)
	if err != nil {
		return nil, err
	}

	err = setHead(st, newCommit, "cherry-pick")
	if err != nil {
		return nil, err
	}

	err = st.repo.StateCleanup()
	if err != nil {
		return nil, err
	}

	return newCommit, nil
}

func addOrRemove(repo *git.Repository, index *git.Index, path string) error {
	_, err := os.Stat(workdirFile(repo, path))
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Removing %s\n", path)
			return index.RemoveByPath(path)
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "Adding %s\n", path)
	return index.AddByPath(path)
}

func handleChanges(st state) error {
	files, err := changedFiles(st.repo)
	if err != nil {
		return err
	}

	index, err := st.repo.Index()
	if err != nil {
		return err
	}

	for _, file := range files {
		err = addOrRemove(st.repo, index, file)
		if err != nil {
			return err
		}
	}

	switch st.repo.State() {
	case git.RepositoryStateNone:
		// Amend the last commit
		_, err = makeCommit(st, nil, false)
		if err != nil {
			return err
		}
	case git.RepositoryStateCherrypick:
		// Commit the cherry-pick commit
		commits, err := readCommitsFromFile(st.repo, gitFile(st.repo, "CHERRY_PICK_HEAD"))
		if err != nil {
			return err
		}
		if len(commits) != 1 {
			return errors.New("Invalid CHERRY_PICK_HEAD file")
		}
		_, err = makeCommit(st, commits[0], true)
		if err != nil {
			return err
		}

		err = st.repo.StateCleanup()
		if err != nil {
			return err
		}
	default:
		return errors.New("Invalid repository state")
	}

	return nil
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
			fmt.Fprintf(os.Stderr, "Error: We're not on the original branch `%s` anymore.\n", st.branchName)
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

func checkout(st state, commit *git.Commit, how string) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}

	err = st.repo.CheckoutTree(tree, &git.CheckoutOpts{Strategy: git.CheckoutSafe})
	if err != nil {
		return err
	}

	err = setHead(st, commit, how)
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
			fmt.Fprintf(os.Stderr, "Checking out %s\n", commit.Id())

			err = checkout(st, commit, "checkout")
			if err != nil {
				return err
			}
		} else {
			fmt.Fprintf(os.Stderr, "Cherry-picking %s\n", commit.Id())

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
				fmt.Fprintf(os.Stderr, `Cherry-pick failed with conflicts.
Please fix them and commit with

    git cherry-pick --continue

then continue with

    git polish-history continue
`)
				err = writeState(st)
				if err != nil {
					return err
				}
				return nil
			}

			makeCommit(st, commit, false)
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

	fmt.Fprintf(os.Stderr, "Done.\n")

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
		if !c.Bool("automatic") {
			fmt.Fprintf(os.Stderr, "\nPlease stash, commit, or remove them.\n")
			os.Exit(1)
		}
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

		if c.GlobalIsSet("test") {
			st.buildCommand = c.GlobalString("test")
			st.buildDirectory = cwd
		}

		builds, err := tryBuild(st)
		if err != nil {
			return err
		}

		if !builds {
			fmt.Fprintf(os.Stderr, `The build failed.
Please fix it by amending the last commit.
Then continue with

    git polish-history continue
`)
			os.Exit(1)
		}

		if changes {
			fmt.Fprintf(os.Stderr, "\nAutomatically committing them.\n")
			handleChanges(st)
		}
	} else {
		if err == nil {
			fmt.Fprintf(os.Stderr, `Error: There is already a polish-history in progress.
If you want to continue with it, use

    git polish-history continue

or abort it with

    git polish-history abort
`)
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
			buildCommand:   c.GlobalString("test"),
			buildDirectory: cwd,
			commits:        commits,
		}

		err = checkout(st, startCommit, "start")
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

func appAction(c *cli.Context) {
	cli.ShowAppHelp(c)
	os.Exit(1)
}

func startAction(c *cli.Context) error {
	return appActualAction(c, false)
}

func continueAction(c *cli.Context) error {
	return appActualAction(c, true)
}

func abortAction(c *cli.Context) error {
	repo, err := openRepo()
	if err != nil {
		return err
	}

	st, err := readState(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, `Error: Could not read current state: %v
Is there really a polish-history in progress?
`, err)
		os.Exit(1)
	}

	changes, err := hasChanges(repo)
	if err != nil {
		return err
	}
	if changes {
		fmt.Fprintf(os.Stderr, `Refusing to abort.
Please stash or remove them.
`)
		os.Exit(1)
	}

	if len(st.commits) > 0 {
		err = checkout(st, st.commits[0], "abort")
		if err != nil {
			return err
		}
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
	app.Version = "0.1"
	app.Usage = "Reform history by fixing the build on each commit"
	app.Author = "Mark Probst"
	app.Email = "mark.probst@gmail.com"
	app.Commands = []cli.Command{
		{
			Name:   "start",
			Usage:  "Start from a given commit",
			Action: actionRunner(startAction),
		},
		{
			Name:   "continue",
			Usage:  "Continue current run",
			Action: actionRunner(continueAction),
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "automatic,a",
					Usage: "Automatically amend or finish cherry-pick",
				},
			},
		},
		{
			Name:   "abort",
			Usage:  "Abort current run",
			Action: actionRunner(abortAction),
		},
	}
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "test",
			Value: "make -j4",
			Usage: "Build/test command",
		},
	}
	app.Action = appAction

	app.Run(os.Args)
}
