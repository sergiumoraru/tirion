class Foo {
  bar() {
    this.baz();
    this.service.qux();
    super.quux();
  }

  baz() {}
}

function standalone() {
  const foo = new Foo();
  foo.baz();
}
