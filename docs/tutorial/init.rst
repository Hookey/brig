Creating a repository
---------------------

You need a central place where ``brig`` stores its metadata. This place is
called a »repository« or short »repo«. This is not the place, where your files
are stored. Those are copied (if you did setup IPFS in a normal way) to
``~/.ipfs``. Keep in mind that ``brig`` will copy files and thus will never
modify the original files on your hard drive.

By creating a new repository you also generate your identity, under which your
buddies can later **find** and **authenticate** you. But enough of the mere
theory, let's get started:

.. code-block:: bash

    # Create a place where we store our metadata.
    # The repository is created by default in the current working directory.
    # (This can be changed via `brig --repo`)

    $ mkdir repo && cd repo
    $ brig init ali@woods.org/desktop -w 'echo my-password'

           _____         /  /\        ___          /  /\
          /  /::\       /  /::\      /  /\        /  /:/_
         /  /:/\:\     /  /:/\:\    /  /:/       /  /:/ /\
        /  /:/~/::\   /  /:/~/:/   /__/::\      /  /:/_/::\
       /__/:/ /:/\:| /__/:/ /:/___ \__\/\:\__  /__/:/__\/\:\
       \  \:\/:/~/:/ \  \:\/:::::/    \  \:\/\ \  \:\ /~~/:/
        \  \::/ /:/   \  \::/~~~~      \__\::/  \  \:\  /:/
         \  \:\/:/     \  \:\          /__/:/    \  \:\/:/
          \  \::/       \  \:\         \__\/      \  \::/
           \__\/         \__\/                     \__\/


         A new file README.md was automatically added.
         Use 'brig cat README.md' to view it & get started.

    $ ls
    config.yml  gateway    immutable.yml  keyring
    metadata    README.md  remotes.yml

The name you specified after the ``init`` is the name that will be shown
to other users and by which you are searchable in the network.
See :ref:`about_names` for more details on the subject.

Once the ``init`` ran successfully there will be a daemon process running in
the background. Every other ``brig`` commands will communicate with it via
a local network socket. If the daemon does not run yet, it will be started for
you in the background without you noticing.

.. note::

   If no IPFS daemon is running, ``brig`` will start one for you. If you don't
   have ``ipfs`` installed, it will even install and set it up for you. By
   default, ``brig init`` will also set some default options that help ``brig``
   to run a bit smoother. If you do not want those, please add
   ``--no-ipfs-optimization`` to the ``init`` command above.

.. _about_names:

Choosing and finding names
~~~~~~~~~~~~~~~~~~~~~~~~~~

You might wonder what the name you pass to ``init`` is actually for. As
previously noted, there is no real restriction for choosing a name, so all of
the following are indeed valid names:

- ``ali``
- ``ali@woods.org``
- ``ali@woods.org/desktop``
- ``ali/desktop``

It's however recommended to choose a name that is formatted like
a XMPP/Jabber-ID. Those IDs can look like plain emails, but can optionally have
a »resource« part as suffix (separated by a »/« like ``desktop``). Choosing
such a name has two advantages:

- Other peers can find you by only specifying parts of your name.
  Imagine all of the *Smith* family members use ``brig``, then they'd possibly those names:

  * ``dad@smith.org/desktop``
  * ``mom@smith.org/tablet``
  * ``son@smith.org/laptop``

  When ``dad`` now sets up ``brig`` on his server, he can use ``brig net locate
  -m domain 'smith.org'`` to get all fingerprints of all family members. Note
  however that ``brig net locate`` **is not secure**. Its purpose is solely
  discovery, but is not able to verify that the fingerprints really correspond
  to the persons they claim to be. This due to the distributed nature of
  ``brig`` where there is no central or federated authority that coordinate
  user name registrations. So it is perfectly possible that one name can be
  taken by several repositories - only the fingerprint is unique.

- Later development of ``brig`` might interpret the user name and domain as
  email and might use your email account for verification purposes.

Having a resource part is optional, but can help if you have several instances
of ``brig`` on your machines. i.e. one user name could be
``dad@smith.org/desktop`` and the other ``dad@smith.org/server``.


Running the daemon and viewing logs
-----------------------------------

The following sections are not a required read. They are useful to keep in
mind, but in the ideal case you're don't even need to think about the daemon.

As discussed before, the daemon is being started on demand in the background.
Subsequent commands will then use the daemon. For debugging purposes it can be useful
to run in the daemon in the foreground. You can do this with the ``brig daemon`` commands:

.. code-block:: bash

    # Make sure no prior daemon is running:
    $ brig daemon quit
    # Start the daemon in the foreground and log to stdout:
    $ brig daemon launch -s

If you want to quit the instance, either just hit CTRL-C or type ``brig daemon
quit`` into another terminal window.

Logging
~~~~~~~

Unless you pass the ``-s`` (``--log-to-stdout`` flag) as above, all logs are
being piped to the system log. You can follow the log like this:

.. code-block:: bash

    # Follow the actual daemon log:
    $ journalctl -ft brig

This assumes you're using a ``systemd``-based distribution. If not, refer to
the documentation of your syslog daemon.

Using several repositories in parallel
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

It can be useful to run more than one instance of the ``brig`` daemon in
parallel. Either for testing purposes or as actual production configuration. In
order for the ``brig`` client to know what daemon to talk to, you have to be
specific about the repository (``--repo``) path. Here is an example:

.. code-block:: bash

   # Be explicit
   $ brig --repo /tmp/ali init ali -x --ipfs-path ~/.ipfs
   $ brig --repo /tmp/bob init bob -x --ipfs-path ~/.ipfs2

   # Since you specified --repo we know what daemon to talk to.
   # You can also set BRIG_PATH for the same effect:
   $ BRIG_PATH=/tmp/ali brig ls
   <file list of ali>

   # Add some alias to your .bashrc to save you some typing:
   $ alias brig-ali="brig --repo /tmp/ali"
   $ alias brig-bob="brig --repo /tmp/bob"

   # Now you can use them normally,
   # e.g. by adding them as remotes each:
   $ brig-ali remote add bob $(brig-bob whoami -f)
   $ brig-bob remote add ali $(brig-ali whoami -f)


.. note::

   It is possible to have several repositories per IPFS instances. Since things
   might get confusing though when it comes to pinning, it is recommended to
   have several IPFS daemons running in this case. This is done via the
   ``--ipfs-port`` flag in the example above.

Locking the repository.
-----------------------

The repository on disk is not encrypted. If you plan on moving the repository
to somewhere else, e.g. by copying it onto an USB stick and physically moving
it somewhere else you should always consider to first create an encrypted
archive out of it and unpack it on the target machine. ``brig`` has a built-in
helper for this. Please refer to ``brig pack-repo --help`` and ``brig
unpack-repo --help``.
