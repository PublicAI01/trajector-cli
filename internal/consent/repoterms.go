package consent

import "errors"

// RepoTermsVersion identifies RepoTermsText. It is the day the text was
// written. A submission states the version the user accepted, and a
// change to the text needs a new version, so that every submission
// names the terms it was sent under.
const RepoTermsVersion = "2026-11-16"

// RepoTermsText is shown in full before `trajector repo assess
// --submit` sends anything, until the user accepts this version. It is
// separate from AgreementText: accepting one does not accept the
// other, and changing one does not ask anyone to accept the other
// again. Like AgreementText it states what the client does and must
// stay true to it; PRIVACY.md describes the same command.
const RepoTermsText = `Trajector Repository Assessment Terms (` + RepoTermsVersion + `)

These terms cover one command: trajector repo assess --submit. They are
separate from the data contribution agreement and do not change it.

1. What is sent. When you confirm, trajector sends one submission
   about the repository you named to the trajector service:
   (a) the readout this command printed: counts and shares computed
       from the repository's history and file paths;
   (b) the id of the repository's first commit and of the commit HEAD
       points to, and for each remote only the kind of host it is on:
       github, gitlab, bitbucket or other. If any of this code is
       public, these commit ids can identify it;
   (c) for each commit, newest first, up to the number the preview
       shows: the commit id, the author time, a number that stands for
       the author, whether a coding agent made or co-authored it, the
       commit message without the lines that name a person (such as
       Co-authored-by and Signed-off-by), and the changed file paths
       with the number of lines added and deleted in each;
   (d) your four answers: who owns the repository, whether it was ever
       public, whether it runs in production, and how much of it an AI
       wrote.
   Commit messages and file paths pass the same local masking as all
   other trajector uploads. Masking finds secrets, email addresses and
   phone numbers by their form. Any other name, path, URL or word
   written in a commit message is sent as it was written: masking
   cannot know which of them are confidential to you. The preview file
   shows everything that is sent, as it is sent.

2. What is not sent. No file content and no diff. No author name or
   email address of any commit, and no message line that names a
   person, as in 1(c). No remote URL and no host name of a remote. No
   location of the repository on your machine: the changed paths are
   paths inside the repository. What a commit message itself says is
   sent as in 1. Nothing is sent before you confirm, and nothing is
   sent again unless you run the command again.

3. What it is used for. Trajector uses a submission to assess the
   repository and to decide whether to contact you about buying or
   licensing it, at the email address of your trajector account.
   Trajector may also use submissions in aggregate, in a form that
   identifies no repository and no person, to check and adjust how it
   assesses repositories. It uses a submission for nothing beyond these
   two uses.

4. No rights and no commitment. A submission gives no one any right
   to your code: the repository stays yours. A submission is not an
   offer. It does not commit you to sell or license anything, and it
   does not commit trajector to contact you, to make an offer, or to
   pay anything. A sale or a license needs a separate written
   agreement.

5. Deleting a submission. Email support@publicai.io from the email
   address of your trajector account, with the submission id this
   command printed or the repository's first commit id. Trajector
   then deletes that submission. If you ask us to close your trajector
   account, we delete all of your submissions.

6. Your confirmation. By answering yes you confirm that you have the
   right to share this information about this repository: you wrote
   it on your own, or everyone who wrote it agrees, or you act for the
   company that owns it. Do not submit a repository that belongs to
   someone else, such as an employer or a client, unless they agree.

Trajector records on this device which version of these terms you
agreed to, and when.`

// AcceptedRepoTerms reports the repository assessment terms version
// the user accepted and when; both are empty when none was accepted.
func (s *Store) AcceptedRepoTerms() (version, acceptedAt string, err error) {
	f, err := s.read()
	if err != nil {
		return "", "", err
	}
	return f.RepoTerms.Version, f.RepoTerms.AcceptedAt, nil
}

// AcceptRepoTerms records explicit acceptance of a repository
// assessment terms version. It leaves the agreement record alone.
func (s *Store) AcceptRepoTerms(version, at string) error {
	if version == "" {
		return errors.New("consent: repository assessment terms version is required")
	}
	return s.update(func(f *storeFile) {
		f.RepoTerms = agreementRecord{Version: version, AcceptedAt: at}
	})
}
