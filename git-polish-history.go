package main

import (
	"errors"
	"fmt"
	"github.com/codegangsta/cli"
	git "github.com/schani/gogit"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
)

const toolName = "polish-history"

type state struct {
	repo           *git.Repo
	branchName     string
	buildCommand   string
	buildDirectory string
	commits        []git.Oid
}

const (
	buildCommandFilename   = "build-command"
	buildDirectoryFilename = "build-directory"
	commitsFilename        = "commits"
	branchFilename         = "branch"
)

func gitFile(repo *git.Repo, name string) string {
	return path.Join(repo.Path, ".git", name)
}

func stateDir(repo *git.Repo) string {
	return gitFile(repo, toolName)
}

func stateFile(repo *git.Repo, name string) string {
	return path.Join(stateDir(repo), name)
}

func workdirFile(repo *git.Repo, name string) string {
	return path.Join(repo.Path, name)
}

func branchName(repo *git.Repo) (string, error) {
	name, err := repo.RevParseAbbrev("HEAD")
	if err != nil {
		return "", err
	}
	if name == "HEAD" {
		return "", nil
	}

	return name, nil
}

func readStateFile(repo *git.Repo, name string) (string, error) {
	bytes, err := ioutil.ReadFile(stateFile(repo, name))
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func writeStateFile(repo *git.Repo, name string, contents string) error {
	return ioutil.WriteFile(stateFile(repo, name), []byte(contents), 0644)
}

func readCommitsFromFile(repo *git.Repo, path string) ([]git.Oid, error) {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	str := strings.TrimSpace(string(bytes))

	commitIds := strings.Fields(str)
	commits := []git.Oid{}
	for _, commitId := range commitIds {
		commits = append(commits, git.Oid(commitId))
	}

	return commits, nil
}

func readState(repo *git.Repo) (state, error) {
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
		commitIds = append(commitIds, string(commit))
	}

	err = writeStateFile(st.repo, commitsFilename, strings.Join(commitIds, "\n"))
	if err != nil {
		return err
	}

	return nil
}

func deleteState(repo *git.Repo) error {
	return os.RemoveAll(stateDir(repo))
}

func filesToBeStaged(repo *git.Repo) ([]string, error) {
	statuses, err := repo.Status()
	if err != nil {
		return nil, err
	}

	files := []string{}
	for _, status := range statuses {
		if status.WorkTreeStatus != git.StatusFlagUnmodified {
			if status.NewPath != "" {
				return nil, errors.New("Don't know how to handle worktree rename")
			}
			files = append(files, status.OldPath)
		}
	}

	return files, nil
}

func hasChanges(repo *git.Repo) (bool, error) {
	// FIXME: We can actually use commit -a to do this when we're
	// committing.

	// FIXME: We're doing Status here as well as in
	// filesToBeStaged.  Only do it once.
	statuses, err := repo.Status()
	if err != nil {
		return true, err
	}

	if len(statuses) == 0 {
		return false, nil
	}

	fmt.Fprintf(os.Stderr, "Changes in working directory and/or index:\n\n")
	for _, status := range statuses {
		if status.NewPath != "" {
			fmt.Fprintf(os.Stderr, "\t%s -> %s\n", status.OldPath, status.NewPath)
		} else {
			fmt.Fprintf(os.Stderr, "\t%s\n", status.OldPath)
		}
	}

	return true, nil
}

func handleChanges(st state) error {
	files, err := filesToBeStaged(st.repo)
	if err != nil {
		return err
	}

	for _, file := range files {
		err = st.repo.Add(file)
		if err != nil {
			return err
		}
	}

	repoState, err := st.repo.State()
	if err != nil {
		return err
	}

	switch repoState {
	case git.StateNone:
		return st.repo.CommitAmend()
	case git.StateCherryPick:
		commit, err := st.repo.CherryPickHead()
		if err != nil {
			return err
		}
		err = st.repo.RemoveGitFile("CHERRY_PICK_HEAD")
		if err != nil {
			return err
		}
		present, err := st.repo.HasGitFile("COMMIT_EDITMSG")
		if err != nil {
			return err
		}
		if present {
			err = st.repo.RemoveGitFile("COMMIT_EDITMSG")
			if err != nil {
				return err
			}
		}
		return st.repo.CommitReuse(commit)
	default:
		return errors.New("Don't know how to handle repository state")
	}
}

func checkout(st state, commit git.Oid, how string) error {
	return st.repo.ResetHard(commit)
}

func getCommits(repo *git.Repo, startName string) ([]git.Oid, error) {
	start, err := repo.RevParse(startName)
	if err != nil {
		return nil, err
	}

	current, err := repo.RevParse("HEAD")
	if err != nil {
		return nil, err
	}

	commits := []git.Oid{}
	for current != start {
		parents, err := repo.Parents(current)
		if err != nil {
			return nil, err
		}

		if len(parents) == 0 {
			return nil, fmt.Errorf("History does not contain start commit `%s`", startName)
		}
		if len(parents) != 1 {
			return nil, fmt.Errorf("Cannot handle commits with more than one parent: `%s`", current)
		}

		commits = append(commits, current)

		current = parents[0]
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

		parents, err := st.repo.Parents(commit)
		if err != nil {
			return err
		}

		if len(parents) != 1 {
			return fmt.Errorf("Commit `%s` should have exactly one parent.", commit)
		}
		parent := parents[0]

		head, err := st.repo.RevParse("HEAD")
		if err != nil {
			return err
		}

		// If HEAD is the same as the next commit's parent, we
		// can just checkout out that commit.  Otherwise we
		// have to cherry-pick it.
		if head == parent {
			fmt.Fprintf(os.Stderr, "Checking out %s\n", commit)

			err = checkout(st, commit, "checkout")
			if err != nil {
				return err
			}
		} else {
			fmt.Fprintf(os.Stderr, "Cherry-picking %s\n", commit)

			clean, err := st.repo.CherryPick(commit)
			if err != nil {
				return err
			}

			if !clean {
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

	repo, err := git.Repository("")
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
			err = handleChanges(st)
			if err != nil {
				fmt.Fprintf(os.Stderr, `Error: There was a problem committing the changes.
Please check

    git status

and resolve the issue manually, then continue with

    git polish-history continue
`)
				os.Exit(1)
			}
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

		startCommit, err := repo.RevParse(startCommitName)
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
	repo, err := git.Repository("")
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
	app.Version = "0.2"
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
