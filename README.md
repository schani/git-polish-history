# git-polish-history

A tool to interactively rewrite history to fix build or test errors.

## Introduction

Often, when working on private branches with long histories, it so
happens that a commit somewhere in between breaks the build, or some
tests don't pass anymore.  If you know which commit it is you can fix
it with `git-rebase`.  But maybe you don't know.  Maybe you aren't
even sure whether all the commits in your history build and pass the
tests.

`git-polish-history` to the rescue!  Let's say you are on a private
feature branch with quite a few commits that you are unsure about.
The last one that's been pushed is `last-pushed-commit`.  To check
whether your code compiles and passes all the tests you can run `make
check`.  Let's automate this:

    git polish-history --test="make check" start last-pushed-commit

`git-polish-history` will now go through all commits since
`last-pushed-commit` and run `make check` on them.  On the first one
that fails it will stop and tell you to fix the problem.  You can do
this any way you want as long as you commit your changes.  Typically
you'd amend the last commit.  Once you're done, you do

    git polish-history continue

and the process continues.  It will stop again whenever a commit fails
the test, when a merge conflict occurs, or when it's done.  If you
want it to do the committing of your changes automatically, use

    git polish-history continue --automatic

## Shortcomings

Right now only linear histories are supported.  That is, if there is
some merging going on between your current branch and the commit you
want to start the polishing at, `git-polish-history` will complain and
refuse to work.

Checking for local changes is very slow.  I've tried using the status
API instead of the diff API, but it doesn't work on a particular
repository I'm working on, and I'm not yet sure why.

It will commit empty cherry-picks.
