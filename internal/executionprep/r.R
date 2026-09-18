# Parse only: the candidate's source is never evaluated, sourced or imported.
input <- paste(readLines(file("stdin"), warn=FALSE), collapse="\n")
if (nchar(input, type="bytes") > 262144L) stop("source too large")
tree <- parse(text=input, keep.source=TRUE)
bindings <- new.env(parent=emptyenv())
aliases <- new.env(parent=emptyenv())
facts <- 0L
hex <- function(value) paste(sprintf("%02x", as.integer(charToRaw(enc2utf8(value)))), collapse="")
emit <- function(kind, name, args=character()) {
  if (facts >= 256L) return(invisible(NULL))
  facts <<- facts+1L
  cat(paste(c(kind, hex(name), vapply(args, hex, "")), collapse="\t"), "\n", sep="")
}
literal <- function(value) {
  if (is.character(value)) return(value)
  if (is.symbol(value) && exists(as.character(value), bindings, inherits=FALSE))
    return(get(as.character(value), bindings, inherits=FALSE))
  if (is.call(value) && identical(value[[1L]], as.name("c"))) {
    parts <- lapply(as.list(value)[-1L], literal)
    if (all(vapply(parts, is.character, TRUE))) return(unlist(parts, use.names=FALSE))
  }
  NULL
}
target <- function(value) {
  if (is.symbol(value)) {
    name <- as.character(value)
    if (exists(name, aliases, inherits=FALSE)) return(get(name, aliases, inherits=FALSE))
    return(name)
  }
  if (is.call(value) && length(value)==3L && identical(value[[1L]], as.name("::")))
    return(paste0(as.character(value[[2L]]), "::", as.character(value[[3L]])))
  ""
}
walk <- function(value, depth=0L) {
  if (depth>64L || facts>=256L) return(invisible(NULL))
  if (is.expression(value)) { for (child in value) walk(child, depth+1L); return(invisible(NULL)) }
  if (!is.call(value)) return(invisible(NULL))
  name <- target(value[[1L]])
  if (name %in% c("<-", "=") && length(value)==3L && is.symbol(value[[2L]])) {
    assign(as.character(value[[2L]]), literal(value[[3L]]), bindings)
    name2 <- as.character(value[[2L]])
    binding <- target(value[[3L]])
    if (nzchar(binding)) assign(name2, binding, aliases)
    else if (exists(name2, aliases, inherits=FALSE)) rm(list=name2, envir=aliases)
  }
  args <- as.list(value)[-1L]
  first <- if (length(args)) literal(args[[1L]]) else NULL
  if (name %in% c("system", "base::system") && length(first)==1L) emit("source", "bash", first)
  else if (name %in% c("system2", "base::system2") && length(first)==1L) {
    rest <- if (length(args)>1L) literal(args[[2L]]) else character()
    if (!is.null(rest)) emit("process", "", c(first, rest))
  } else if (name %in% c("source", "sys.source", "base::source") && length(first)==1L) emit("script", "r", first)
  else if (nzchar(name)) emit("call", name, if (length(first)) first[[1L]] else character())
  if (name=="function") return(invisible(NULL))
  for (child in args) walk(child, depth+1L)
}
walk(tree)
