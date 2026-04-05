# Subcommands
complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "start" \
  --description "create and start a new worktree session"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "resume" \
  --description "resume an existing worktree session"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "attach" \
  --description "create a worktree and attach"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "merge" \
  --description "merge current worktree into main"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "close" \
  --description "close a session without merging"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "clean" \
  --description "remove merged worktrees"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "list" \
  --description "list tracked sessions"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "pull" \
  --description "pull repos and rebase worktrees"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "perms" \
  --description "manage Claude Code permission tiers"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "fork" \
  --description "fork current worktree into a new branch"

complete \
  --command spinclass \
  --no-files \
  --condition __fish_use_subcommand \
  --arguments "validate" \
  --description "validate worktree configuration"

# Global flags
complete \
  --command spinclass \
  --no-files \
  --long-option format \
  --require-parameter \
  --arguments "tap table" \
  --description "output format"

# Dynamic target completions for start/resume/attach/merge (sessions + local worktrees)
complete \
  --command spinclass \
  --no-files \
  --keep-order \
  --condition "__fish_seen_subcommand_from start resume attach merge close" \
  --arguments "(spinclass completions --sessions; spinclass completions)"

# start flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from start" \
  --long-option merge-on-close \
  --description "merge worktree on session close"

complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from start" \
  --long-option no-attach \
  --description "create worktree without attaching"

complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from start" \
  --long-option pr \
  --require-parameter \
  --arguments "(spinclass completions --prs)" \
  --description "start session from a PR"

# resume flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from resume" \
  --long-option no-attach \
  --description "find session without attaching"

# close flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from close" \
  --short-option f \
  --long-option force \
  --description "skip confirmation for unpushed branches"

# attach flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from attach" \
  --long-option merge-on-close \
  --description "merge worktree on session close"

complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from attach" \
  --long-option no-attach \
  --description "create worktree without attaching"

# merge flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from merge" \
  --long-option git-sync \
  --description "sync with remote before merging"

# clean flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from clean" \
  --short-option i \
  --long-option interactive \
  --description "interactively discard changes in dirty merged worktrees"

# pull flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from pull" \
  --short-option d \
  --long-option dirty \
  --description "include dirty repos and worktrees"

# fork flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from fork" \
  --long-option from \
  --require-parameter \
  --description "source worktree directory"

# perms subcommands
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from perms; and not __fish_seen_subcommand_from list edit review" \
  --arguments "list" \
  --description "list permission tier rules"

complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from perms; and not __fish_seen_subcommand_from list edit review" \
  --arguments "edit" \
  --description "edit a permission tier file"

complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from perms; and not __fish_seen_subcommand_from list edit review" \
  --arguments "review" \
  --description "review new permissions from a session"

# perms list/edit flags
complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from list edit" \
  --long-option repo \
  --require-parameter \
  --description "repo name"

complete \
  --command spinclass \
  --no-files \
  --condition "__fish_seen_subcommand_from edit" \
  --long-option global \
  --description "edit the global tier file"
