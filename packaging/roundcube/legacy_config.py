"""Parse the literal-only v80 Roundcube configuration without executing PHP."""
import re


class LiteralConfigParser:
    def __init__(self, text):
        self.text = text
        self.position = 0

    def whitespace(self):
        while self.position < len(self.text):
            match = re.match(r'\s+|/\*.*?\*/|//[^\n]*|\#[^\n]*|<\?php|\?>', self.text[self.position:], re.S)
            if not match:
                break
            self.position += match.end()

    def take(self, token):
        self.whitespace()
        if self.text.startswith(token, self.position):
            self.position += len(token)
            return True
        return False

    def require(self, token):
        if not self.take(token):
            raise ValueError('Unsupported literal configuration syntax')

    def string(self):
        self.whitespace()
        if self.position >= len(self.text) or self.text[self.position] not in "'\"":
            raise ValueError('Configuration key/value must be a literal string')
        delimiter = self.text[self.position]
        self.position += 1
        result = []
        while self.position < len(self.text):
            char = self.text[self.position]
            self.position += 1
            if char == delimiter:
                return ''.join(result)
            if delimiter == '"' and char == '$':
                raise ValueError('Interpolated configuration is not supported')
            if char == '\\':
                if self.position >= len(self.text):
                    break
                following = self.text[self.position]
                self.position += 1
                if following in ('\\', delimiter):
                    result.append(following)
                elif delimiter == "'":
                    result.extend(('\\', following))
                else:
                    raise ValueError('Nonliteral string escape is not supported')
            else:
                result.append(char)
        raise ValueError('Unterminated configuration string')

    def value(self):
        self.whitespace()
        if self.position < len(self.text) and self.text[self.position] in "'\"":
            return self.string()
        if self.take('array'):
            self.require('(')
            return self.array(')')
        if self.take('['):
            return self.array(']')
        for token, value in [('true', True), ('false', False), ('null', None)]:
            if self.text.startswith(token, self.position):
                end = self.position + len(token)
                if end == len(self.text) or not re.match(r'[A-Za-z0-9_]', self.text[end]):
                    self.position = end
                    return value
        match = re.match(r'-?(?:0|[1-9][0-9]*)(?![A-Za-z0-9_.])', self.text[self.position:])
        if match:
            self.position += match.end()
            return int(match.group())
        raise ValueError('Executable or computed configuration is not supported')

    def array(self, closing):
        pairs = []
        associative = None
        if self.take(closing):
            return []
        while True:
            first = self.value()
            keyed = self.take('=>')
            if associative is not None and associative != keyed:
                raise ValueError('Mixed configuration arrays are not supported')
            associative = keyed
            if keyed:
                if not isinstance(first, str):
                    raise ValueError('Configuration array keys must be strings')
                pairs.append((first, self.value()))
            else:
                pairs.append(first)
            if self.take(closing):
                break
            self.require(',')
            if self.take(closing):
                break
        if not associative:
            return pairs
        if len({key for key, _ in pairs}) != len(pairs):
            raise ValueError('Duplicate configuration array key')
        return dict(pairs)

    def parse(self):
        values = {}
        initialized = False
        while True:
            self.whitespace()
            if self.position == len(self.text):
                break
            self.require('$config')
            if self.take('['):
                key = self.string()
                self.require(']')
                self.require('=')
                if key in values:
                    raise ValueError('Ambiguous duplicate configuration assignment')
                values[key] = self.value()
            else:
                self.require('=')
                if initialized or values or self.value() not in ({}, []):
                    raise ValueError('Configuration must have a single empty initialization')
                initialized = True
            self.require(';')
        return values


def parse_php_config(text):
    if len(text.encode()) > 262144:
        raise ValueError('Legacy configuration exceeds the fixed size limit')
    return LiteralConfigParser(text).parse()
