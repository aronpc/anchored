package updater

// oNoFollow has no Windows equivalent. O_EXCL still carries the load there:
// it refuses to open an existing path at all, symlink or not.
const oNoFollow = 0
