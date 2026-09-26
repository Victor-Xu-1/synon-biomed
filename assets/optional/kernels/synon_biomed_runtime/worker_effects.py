"""Discharge host-created observation binding obligations in this interpreter.

Source classification belongs to the host's native AST preparation. This module
does not classify arbitrary code as safe, change namespaces, or replace exec.
The manager independently retains provenance outside this mutable process.
"""
import builtins
import hashlib
import os
import sys
import types


class ObservationGuard:
    def __init__(self):
        self.tainted = False
        self.output = builtins.print
        self.stdout = sys.stdout
        self.stderr = sys.stderr

    def validate(self, request, namespace):
        proof = request.get('observation')
        if proof is None:
            # A failed/partial arbitrary cell can already have changed bindings.
            self.tainted = True
            return None
        if self.tainted:
            return 'runtime_binding_provenance_unproved'
        if type(proof) is not dict or proof.get('schema') != 'synon.execution-observation.v1':
            return 'diagnostic_contract_invalid'
        source = request['code']
        if hashlib.sha256(source.encode('utf-8')).hexdigest() != request.get('observation_code_sha256'):
            return 'diagnostic_source_binding_mismatch'
        language = proof.get('language')
        if language not in ('python', 'bash'):
            return 'diagnostic_language_binding_mismatch'
        if language == 'python' and proof.get('source_sha256') != request.get('observation_code_sha256'):
            return 'diagnostic_source_binding_mismatch'
        operations = proof.get('operations')
        registry = {'python.literal', 'python.output'} if language == 'python' else {
            'bash.directory', 'bash.output', 'bash.format'}
        if type(operations) is not list or not operations or any(type(op) is not str or op not in registry for op in operations):
            return 'diagnostic_operation_unproved'
        if (namespace.get('__builtins__') is not builtins or sys.gettrace() is not None or
                sys.getprofile() is not None or sys.stdout is not self.stdout or sys.stderr is not self.stderr):
            return 'diagnostic_implicit_binding_unproved'
        if 'python.output' in operations:
            # Never invoke a lookup protocol or callable to test identity.
            actual = namespace.get('print', builtins.print)
            if (actual is not self.output or builtins.print is not self.output or
                    type(actual) is not types.BuiltinFunctionType or actual.__module__ != 'builtins' or actual.__name__ != 'print'):
                return 'diagnostic_binding_unproved'
        if language == 'bash':
            # Even though the canonical launch rejects inherited functions,
            # report a shadow instead of silently substituting its builtin.
            names = {'bash.directory': 'pwd', 'bash.output': 'echo', 'bash.format': 'printf'}
            if any('BASH_FUNC_' + names[op] + '%%' in os.environ for op in operations):
                return 'diagnostic_shell_binding_unproved'
        return None


def declined(reason):
    return {
        'schema': 'synon.execution-observation.v1', 'ok': False,
        'status': 'implementation_selection_required', 'executed': False,
        'decision_required': True, 'reason': reason,
        'message': 'Diagnostic runtime bindings are unproved; scientific implementation selection remains required.',
        'recovery': 'Inspect the existing runtime and complete the pending implementation choice. Do not retry unchanged or reset the session implicitly.',
    }
