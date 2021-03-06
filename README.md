[![Build Status](https://travis-ci.org/schani/git-polish-history.svg?branch=master)](https://travis-ci.org/schani/git-polish-history)

# git-polish-history

A tool for interactively rewriting history to fix the build or test
suite failures.

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

## Installation

If you're on OS X, get [Homebrew](http://brew.sh/) and do

    brew tap schani/schani
	brew install git-polish-history

## Shortcomings

Right now only linear histories are supported.  That is, if there is
some merging going on between your current branch and the commit you
want to start the polishing at, `git-polish-history` will complain and
refuse to work.
